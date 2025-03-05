package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/kataras/iris/v12"
	"goftp.io/server/v2"
	"goftp.io/server/v2/driver/file"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	FaxDir      = "/synergyfaxq" // Remote FTP folder
	JobIDPrefix = ""
)

// --- Approach 1: Using part of the UUID ---

// getJobIdFromUUID extracts the last section of a UUID string,
// converts it from hex, and returns a number in the range 1 to 32000.
func getJobIdFromUUID(uuidStr string) (int, error) {
	parts := strings.Split(uuidStr, "-")
	if len(parts) == 0 {
		return 0, errors.New("invalid uuid format")
	}
	lastPart := parts[len(parts)-1]
	// Convert the last section from hex to an integer.
	num, err := strconv.ParseInt(lastPart, 16, 64)
	if err != nil {
		return 0, err
	}
	// Calculate jobId as modulo 32000. If the result is zero, set to 1.
	jobId := int(num % 32000)
	if jobId == 0 {
		jobId = 1
	}
	return jobId, nil
}

// --- Approach 2: Global Counter with Reset ---

var (
	jobCounter = 0
	jobMutex   sync.Mutex
)

// getNextJobId returns a new job id by incrementing a global counter.
// It resets to 1 if the counter exceeds 32000.
func getNextJobId() int {
	jobMutex.Lock()
	defer jobMutex.Unlock()
	jobCounter++
	if jobCounter >= 32000 {
		jobCounter = 1
	}
	return jobCounter
}

// -------------------------------------
// IN-MEMORY TRACKING STRUCTURES
// -------------------------------------

// FaxJobRecord tracks a fax job (sent or received).
type FaxJobRecord struct {
	ReceivedUUID  string    // For received faxes
	CallUUID      string    // Unique key (from payload) used to correlate notifications
	HylafaxJobID  string    // Generated Hylafax job ID (e.g. "fax1234")
	PdfPath       string    // Local path of saved PDF file
	RecvPath      string    // Local path of created .recv file
	LastStatus    string    // Status (e.g. "received", "sent", "completed", "failed", etc.)
	ReceivedAt    time.Time // When the fax was received/submitted
	LastUpdatedAt time.Time // Last update time
}

// Global map to track received and sent faxes by a unique key (here CallUUID)
var (
	faxRecords      = make(map[string]*FaxJobRecord)
	faxRecordsMutex sync.Mutex
)

// Global job queue for sent faxes.
// For sending, we can map the Synergy fax job ID (e.g. derived from an SFC filename) to the Hylafax job ID.
var jobQueue = struct {
	sync.Mutex
	entries map[string]string // synergyJobID -> hylafaxJobID
}{entries: make(map[string]string)}

// -------------------------------------
// DATA STRUCTURES FOR PAYLOADS
// -------------------------------------

// WebhookPayload is used by /fax-notify endpoint.
type WebhookPayload struct {
	FaxJobResults FaxJobResults `json:"fax_job_results"`
	FileData      string        `json:"file_data"`
}

type FaxJobResults struct {
	Results map[string]FaxJob `json:"results"`
	FaxJob  FaxJob            `json:"fax_job"`
}

type FaxJob struct {
	UUID          string        `json:"uuid"`
	CallUUID      string        `json:"call_uuid"`
	SrcTenantID   int           `json:"src_tenant_id"`
	DstTenantID   int           `json:"dst_tenant_id"`
	Number        string        `json:"number"`
	CIDNum        string        `json:"cidnum"`
	CIDName       string        `json:"cidname"`
	Filename      string        `json:"filename"`
	Ident         string        `json:"ident"`
	Header        string        `json:"header"`
	Endpoints     []Endpoint    `json:"endpoints"`
	Result        FaxResult     `json:"result"`
	FaxSourceInfo FaxSourceInfo `json:"fax_source_info"`
	Status        string        `json:"status"`
	TotDials      int           `json:"totdials"`
	NDials        int           `json:"ndials"`
	TotTries      int           `json:"tottries"`
	Ts            string        `json:"ts"`
}

type FaxReceive struct {
	UUID          string        `json:"uuid"`
	CallUUID      string        `json:"call_uuid"`
	SrcTenantID   int           `json:"src_tenant_id"`
	DstTenantID   int           `json:"dst_tenant_id"`
	Number        string        `json:"number"`
	CIDNum        string        `json:"cidnum"`
	CIDName       string        `json:"cidname"`
	Filename      string        `json:"filename"`
	Ident         string        `json:"ident"`
	Header        string        `json:"header"`
	Result        FaxResult     `json:"result"`
	FaxSourceInfo FaxSourceInfo `json:"fax_source_info"`
	Status        string        `json:"status"`
	TotDials      int           `json:"totdials"`
	NDials        int           `json:"ndials"`
	TotTries      int           `json:"tottries"`
	Ts            string        `json:"ts"`
	FileData      string        `json:"file_data"`
}

type Endpoint struct {
	ID           int    `json:"id"`
	Type         string `json:"type"`
	TypeID       int    `json:"type_id"`
	EndpointType string `json:"endpoint_type"`
	Endpoint     string `json:"endpoint"`
	Priority     int    `json:"priority"`
}

type FaxResult struct {
	UUID       string `json:"uuid"`
	StartTs    string `json:"start_ts"`
	EndTs      string `json:"end_ts"`
	Success    bool   `json:"success"`
	ResultCode int    `json:"result_code"`
	ResultText string `json:"result_text"`
}

type FaxSourceInfo struct {
	Timestamp  string `json:"timestamp"`
	SourceType string `json:"source_type"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id"`
}

// -------------------------------------
// SENDING CODE STRUCTURES & CACHE
// -------------------------------------

// sfcFile holds details from an .sfc file.
type sfcFile struct {
	jobID     string
	sfcFile   string
	pdfFile   string
	faxNumber string
}

// cache for SFC and PDF file info while matching pairs.
var cache = struct {
	sync.Mutex
	sfc map[string]sfcFile // pdf filename -> sfcFile details
	pdf map[string]string  // pdf filename -> local file path
}{sfc: make(map[string]sfcFile), pdf: make(map[string]string)}

// -------------------------------------
// MAIN FUNCTION
// -------------------------------------

func main() {
	// Load env variables.
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; proceeding with defaults")
	}

	// Shut down receiving lines when killed
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGTERM, syscall.SIGINT)

	// Start background FTP server and folder watcher.
	/*go startFtp()
	go watchFaxFolder(os.Getenv("FTP_ROOT") + FaxDir)*/
	// Optionally, you can start monitors for .done or .sts files:
	// go monitorDoneFiles(os.Getenv("FTP_ROOT") + FaxDir)
	// go monitorStatusFiles(os.Getenv("FTP_ROOT") + FaxDir)

	app := iris.New()

	// -----------------------------
	// RECEIVING FAXES
	// -----------------------------
	// This endpoint is called when a fax is received.
	app.Post("/fax-receive", func(ctx iris.Context) {
		var fax FaxReceive
		if err := ctx.ReadJSON(&fax); err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": err.Error()})
			return
		}

		// Decode the incoming base64-encoded file data (actual PDF data).
		pdfBytes, err := base64.StdEncoding.DecodeString(fax.FileData)
		if err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": "failed to decode file_data: " + err.Error()})
			return
		}

		// hylafaxJobID := generateJobID()

		uuidParts := strings.Split(fax.UUID, "-")
		if len(uuidParts) == 0 {
			// handle error: invalid UUID format
		}
		baseName := uuidParts[len(uuidParts)-1]

		t := time.Now()
		fileTimestamp := t.Format("20060102150405")

		// Change the file extension to .pdf even if fax.Filename ends with .tiff.
		pdfName := "{" + baseName + "}" + fileTimestamp
		pdfLocalPath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, pdfName+".pdf")

		if err := os.MkdirAll(filepath.Dir(pdfLocalPath), 0755); err != nil {
			ctx.StatusCode(iris.StatusInternalServerError)
			ctx.JSON(iris.Map{"error": "failed to create local directory: " + err.Error()})
			return
		}
		if err := ioutil.WriteFile(pdfLocalPath, pdfBytes, 0644); err != nil {
			ctx.StatusCode(iris.StatusInternalServerError)
			ctx.JSON(iris.Map{"error": "failed to write PDF file: " + err.Error()})
			return
		}
		log.Printf("Saved PDF file to: %s", pdfLocalPath)

		loc, err := time.LoadLocation("America/Vancouver")
		if err != nil {
			log.Fatalf("Failed to load location: %v", err)
		}
		recvTime := time.Now().In(loc).Format("01/02/06 15:04")

		// Create a .recv file which will be used to signal fax receiving.
		recvFilename := pdfName + ".recv"
		recvLocalPath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, recvFilename)
		recvContent := fmt.Sprintf("%s\n%s\n%s\n%s\n",
			recvTime,
			"ttyS0", // Used to correlate sessions.
			pdfName,
			fax.CIDNum,
		)
		if err := ioutil.WriteFile(recvLocalPath, []byte(recvContent), 0644); err != nil {
			ctx.StatusCode(iris.StatusInternalServerError)
			ctx.JSON(iris.Map{"error": "failed to write recv file: " + err.Error()})
			return
		}
		log.Printf("Created recv file: %s", recvLocalPath)

		// Store this received fax in the tracker.
		/*record := &FaxJobRecord{
			ReceivedUUID:  fax.UUID,
			CallUUID:      fax.CallUUID,
			HylafaxJobID:  hylafaxJobID,
			PdfPath:       pdfLocalPath,
			RecvPath:      recvLocalPath,
			LastStatus:    "received",
			ReceivedAt:    time.Now(),
			LastUpdatedAt: time.Now(),
		}
		*/
		ctx.StatusCode(iris.StatusOK)
	})

	// -----------------------------
	// NOTIFICATION ENDPOINT
	// -----------------------------
	// This endpoint is called by the fax-notify system when the status of a fax (sent or received)
	// is updated. Use the CallUUID (or similar unique identifier) to match the notification
	// to an existing fax record.
	// In your /fax-notify endpoint, after updating the in-memory records:
	app.Post("/fax-notify", func(ctx iris.Context) {
		var payload WebhookPayload
		if err := ctx.ReadJSON(&payload); err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": err.Error()})
			return
		}

		// Process each fax job from the notify payload.
		for key, job := range payload.FaxJobResults.Results {
			faxRecordsMutex.Lock()
			if record, exists := faxRecords[job.UUID]; exists {
				record.LastStatus = job.Status
				record.LastUpdatedAt = time.Now()
				log.Printf("Updated fax job %s: new status %s", key, job.Status)
			} else {
				log.Printf("No record found for fax job with UUID: %s", job.UUID)
			}
			faxRecordsMutex.Unlock()

			// For outbound faxes, check if this notify corresponds to a job in our jobQueue.
			jobQueue.Lock()
			for jobUUID, storedHylaFaxID := range jobQueue.entries {
				// Assuming that you can correlate based on the fax UUID or CallUUID,
				// here we check if the notify's UUID matches.
				if job.UUID == jobUUID { // Adjust matching logic as needed.
					// Based on the notify result, create .done or .fail.
					if job.Result.Success {
						log.Printf("Notify indicates fax completed for job %s", jobUUID)
						createStsFile(storedHylaFaxID, "7", "0", "0", "success")
						createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("q%s.done", storedHylaFaxID)), "\r")
					} else {
						log.Printf("Notify indicates fax failed for job %s", jobUUID)
						createStsFile(storedHylaFaxID, "3", "0", "0", "failed")
						createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("q%s.done", storedHylaFaxID)), "\r")
					}
					// Remove job from queue since we've processed it.
					delete(jobQueue.entries, jobUUID)
					break
				}
			}
			jobQueue.Unlock()
		}

		// Also update the overall FaxJob status if present.
		overall := payload.FaxJobResults.FaxJob
		faxRecordsMutex.Lock()
		if record, exists := faxRecords[overall.CallUUID]; exists {
			record.LastStatus = overall.Status
			record.LastUpdatedAt = time.Now()
			log.Printf("Updated overall fax job with CallUUID %s: new status %s", overall.CallUUID, overall.Status)
		}
		faxRecordsMutex.Unlock()

		ctx.StatusCode(iris.StatusOK)
	})

	// -----------------------------
	// SENDING FAXES VIA FTP & WATCHER
	// -----------------------------
	// The sending side uses an FTP server and a watcher on the designated directory.
	// When a file ending with ".sfc" is detected, its content (fax number and PDF file name)
	// is read. If the corresponding PDF is already present (or cached), the fax is submitted.
	// The submitFax function creates a .jobid file, sends the fax via HTTP PUT (to a webhook), and
	// creates .done or .fail files based on the result. It also adds the job to the global job queue.
	// go startFtp()
	go watchFaxFolder(os.Getenv("FTP_ROOT") + FaxDir)

	// -----------------------------
	// START THE WEB SERVER
	// -----------------------------
	app.Listen(":8080")
	select {
	case sig := <-sigchan:
		fmt.Print("Received ", sig, ", killing all channels")
		time.Sleep(3 * time.Second)
		//logger.Logger.Print("Terminating")
		os.Exit(0)
	}
}

func createStsFile(jobID, state, npages, totpages, status string) error {
	stsFilePath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("q%s.sts", jobID))

	// Open (or create) the file in read-write mode.
	file, err := os.OpenFile(stsFilePath, os.O_RDWR|os.O_CREATE, 0660)
	if err != nil {
		return fmt.Errorf("error opening .sts file: %w", err)
	}
	defer file.Close()

	// Read current file contents.
	content, err := ioutil.ReadAll(file)
	if err != nil {
		return fmt.Errorf("error reading .sts file: %w", err)
	}

	// Split the file content into lines.
	lines := strings.Split(string(content), "\n")

	// We'll update the known keys and mark which ones we've seen.
	keysFound := map[string]bool{
		"state":    false,
		"npages":   false,
		"totpages": false,
		"status":   false,
	}

	// Update lines that start with our keys.
	for i, line := range lines {
		if strings.HasPrefix(line, "state:") {
			lines[i] = "state:" + state
			keysFound["state"] = true
		} else if strings.HasPrefix(line, "npages:") {
			lines[i] = "npages:" + npages
			keysFound["npages"] = true
		} else if strings.HasPrefix(line, "totpages:") {
			lines[i] = "totpages:" + totpages
			keysFound["totpages"] = true
		} else if strings.HasPrefix(line, "status:") {
			lines[i] = "status:" + status
			keysFound["status"] = true
		}
	}

	// Append lines for any keys that weren't found.
	if !keysFound["state"] {
		lines = append(lines, "state:"+state)
	}
	if !keysFound["npages"] {
		lines = append(lines, "npages:"+npages)
	}
	if !keysFound["totpages"] {
		lines = append(lines, "totpages:"+totpages)
	}
	if !keysFound["status"] {
		lines = append(lines, "status:"+status)
	}

	newContent := strings.Join(lines, "\n")

	// Seek to the beginning and truncate the file.
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("error seeking in .sts file: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("error truncating .sts file: %w", err)
	}

	// Write the updated content back to the file.
	if _, err := file.WriteString(newContent); err != nil {
		return fmt.Errorf("error writing to .sts file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("error syncing .sts file: %w", err)
	}

	log.Printf(".sts file updated: %s", stsFilePath)
	return nil
}

// -----------------------------
// SENDING & FTP WATCHER FUNCTIONS
// -----------------------------

func startFtp() {
	driver, err := file.NewDriver(os.Getenv("FTP_ROOT"))
	if err != nil {
		log.Fatal(err)
	}

	port, err := strconv.Atoi(os.Getenv("FTP_PORT"))

	s, err := server.NewServer(&server.Options{
		Driver: driver,
		Auth: &server.SimpleAuth{
			Name:     os.Getenv("FTP_USER"),
			Password: os.Getenv("FTP_PASS"),
		},
		Perm:      server.NewSimplePerm("root", "root"),
		RateLimit: 1000000, // 1MB/s limit
		Port:      port,
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func watchFaxFolder(dir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Error creating watcher: %v", err)
	}
	defer watcher.Close()

	err = watcher.Add(dir)
	if err != nil {
		log.Fatalf("Error adding directory to watcher: %v", err)
	}

	log.Printf("Watching directory: %s", dir)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				processFile(event.Name)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

func processFile(filePath string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".sfc":
		handleSfcFile(filePath)
	}
}

func handleSfcFile(filePath string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Error reading SFC file: %v", err)
		return
	}
	log.Printf("SFC Content: %s", string(content))

	lines := strings.Split(string(content), "\n")
	if len(lines) < 2 {
		log.Printf("Invalid SFC file format (len = %d): %s - content: %s", len(lines), filePath, string(content))
		return
	}

	faxNumber := strings.ReplaceAll(lines[0], "\r", "")
	pdfFile := strings.ReplaceAll(lines[1], "\r", "")
	log.Printf("SFC file processed: FaxNumber=%s, PDFFile=%s", faxNumber, pdfFile)

	cache.Lock()
	defer cache.Unlock()
	fax, err := submitFax(faxNumber, pdfFile, filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, pdfFile), filepath.Base(filePath))
	if err != nil {
		log.Printf("Unable to send fax: %s", err)
		return
	}
	cache.sfc[fax] = sfcFile{
		jobID:     fax,
		sfcFile:   filePath,
		pdfFile:   pdfFile,
		faxNumber: faxNumber,
	}
}

// OutboundResponse represents the expected JSON response structure from the PUT request.
type OutboundResponse struct {
	JobUUID string `json:"job_uuid"`
	Message string `json:"message"`
}

// submitFax sends the fax via an HTTP POST request and returns the submitted job UUID.
// If the POST fails (or returns a non-200 response), a .fail file is created immediately.
// submitFax sends the fax via an HTTP POST multipart/form-data request and returns the submitted job UUID.
// If the POST fails (or returns a non-200 response), a .fail file is created immediately.
func submitFax(faxNumber, pdfFile, pdfPath, sfcFileName string) (string, error) {
	jobID := strings.TrimSuffix(sfcFileName, ".sfc")
	hylaJobID := generateJobID() // e.g. "12345678"

	// Create a .jobid file with the generated Hylafax job ID.
	err := createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.jobid", jobID)), hylaJobID+"\r")
	if err != nil {
		log.Printf("Error creating .jobid file: %v", err)
		// Continue even if file creation fails.
	}

	fileData, err := os.ReadFile(pdfPath)
	if err != nil {
		log.Printf("Error reading PDF file: %v", err)
		return "", err
	}

	// Build the multipart form data.
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	// Write form fields.
	if err := writer.WriteField("callee_number", faxNumber); err != nil {
		return "", err
	}
	if err := writer.WriteField("caller_number", os.Getenv("FAX_NUMBER")); err != nil {
		return "", err
	}
	// Create the file field.
	part, err := writer.CreateFormFile("file", pdfFile)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(fileData); err != nil {
		return "", err
	}
	writer.Close()

	// Construct the POST request URL (no query parameters needed now).
	postURL := os.Getenv("SEND_WEBHOOK_URL")
	req, err := http.NewRequest("POST", postURL, &b)
	if err != nil {
		log.Printf("Error creating POST request: %v", err)
		return "", err
	}
	// Set Basic Auth using credentials from environment variables.
	req.SetBasicAuth(os.Getenv("SEND_WEBHOOK_USERNAME"), os.Getenv("SEND_WEBHOOK_PASSWORD"))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending POST request: %v", err)
		// Create the .fail file immediately if the send fails.
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("q%s.done", hylaJobID)), "\r")
		return "", err
	}
	defer resp.Body.Close()

	// Create a .sts file to indicate the fax has been sent.
	if err := createStsFile(hylaJobID, "3", "0", "0", "Sent to WebHook"); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("POST request failed with status: %s", resp.Status)
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("q%s.done", hylaJobID)), "\r")
		return "", fmt.Errorf("fax submission failed with status: %s", resp.Status)
	}

	// Read and decode the response.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %v", err)
		return "", err
	}

	var outResp OutboundResponse
	if err := json.Unmarshal(bodyBytes, &outResp); err != nil {
		log.Printf("Error decoding response JSON: %v", err)
		return "", err
	}

	// For outbound faxes, add the job to the queue for later notify updates.
	addFaxJob(outResp.JobUUID, jobID, hylaJobID)
	log.Printf("Fax submitted successfully: FaxNumber=%s, PDFFile=%s, JobID=%s, Returned Job UUID=%s",
		faxNumber, pdfFile, jobID, outResp.JobUUID)

	return outResp.JobUUID, nil
}

func createFile(filePath, content string) error {
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", filePath, err)
	}
	defer f.Close()

	_, err = f.WriteString(content)
	if err != nil {
		return fmt.Errorf("error writing content to file %s: %w", filePath, err)
	}

	log.Printf("File created with content: %s content: %s", filePath, string(content))
	return nil
}

func addFaxJob(jobUUID, synergyJobID, hylafaxJobID string) {
	jobQueue.Lock()
	defer jobQueue.Unlock()
	jobQueue.entries[jobUUID] = hylafaxJobID
	log.Printf("Fax job added to queue: JobUUID=%s SynergyJobID=%s, HylaFaxJobID=%s", jobUUID, synergyJobID, hylafaxJobID)
}

// generateJobID returns the last 6 characters of a newly generated UUID.
func generateJobID() string {
	// Generate a new UUID.
	id := uuid.New().String() // Example: "123e4567-e89b-12d3-a456-426614174000"
	// Remove hyphens.
	id = strings.ReplaceAll(id, "-", "")
	// Return the last 6 characters.
	if len(id) >= 6 {
		return id[len(id)-6:]
	}
	return id
}

func processStatusFile(filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening .sts file: %v", err)
		return
	}
	defer file.Close()

	var state, npages, totpages, status string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "state:") {
			state = strings.Split(line, ":")[1]
		}
		if strings.Contains(line, "npages:") {
			npages = strings.Split(line, ":")[1]
		}
		if strings.Contains(line, "totpages:") {
			totpages = strings.Split(line, ":")[1]
		}
		if strings.Contains(line, "status:") {
			status = strings.Split(line, ":")[1]
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading .sts file: %v", err)
		return
	}

	switch state {
	case "7":
		log.Printf("Fax completed: NPages=%s, TotPages=%s, Status=%s", npages, totpages, status)
	case "3":
		log.Printf("Fax status: %s (busy, ringing, etc.)", status)
	case "6":
		log.Printf("Fax in progress: NPages=%s, TotPages=%s", npages, totpages)
	default:
		log.Printf("Unknown fax state: %s", state)
	}
	if err := os.Remove(filePath); err != nil {
		log.Printf("Error deleting .sts file: %v", err)
	}
}
