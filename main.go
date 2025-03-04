package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/joho/godotenv"
	"github.com/kataras/iris/v12"
	"goftp.io/server/v2"
	"goftp.io/server/v2/driver/file"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

		// Change the file extension to .pdf even if fax.Filename ends with .tiff.
		baseName := filepath.Base(fax.Filename)
		pdfName := strings.TrimSuffix(baseName, filepath.Ext(baseName)) + ".pdf"
		pdfLocalPath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, pdfName)

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

		// Create a .recv file which will be used to signal fax receiving.
		hylafaxJobID := "fax" + strconv.Itoa(getNextJobId())
		recvFilename := hylafaxJobID + ".recv"
		recvLocalPath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, recvFilename)
		recvContent := fmt.Sprintf("%s\n%s\n%s\n%s\n",
			time.Now().Format("01/02/06 15:04"),
			fax.CallUUID, // Used to correlate sessions.
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
		record := &FaxJobRecord{
			ReceivedUUID:  fax.UUID,
			CallUUID:      fax.CallUUID,
			HylafaxJobID:  hylafaxJobID,
			PdfPath:       pdfLocalPath,
			RecvPath:      recvLocalPath,
			LastStatus:    "received",
			ReceivedAt:    time.Now(),
			LastUpdatedAt: time.Now(),
		}
		faxRecordsMutex.Lock()
		faxRecords[fax.CallUUID] = record
		faxRecordsMutex.Unlock()
		log.Printf("Tracked fax job with CallUUID: %s", fax.CallUUID)

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
			if record, exists := faxRecords[job.CallUUID]; exists {
				record.LastStatus = job.Status
				record.LastUpdatedAt = time.Now()
				log.Printf("Updated fax job %s: new status %s", key, job.Status)
			} else {
				log.Printf("No record found for fax job with CallUUID: %s", job.CallUUID)
			}
			faxRecordsMutex.Unlock()

			// For outbound faxes, check if this notify corresponds to a job in our jobQueue.
			jobQueue.Lock()
			for synergyJobID, storedHylaFaxID := range jobQueue.entries {
				// Assuming that you can correlate based on the fax UUID or CallUUID,
				// here we check if the notify's UUID matches.
				if job.UUID == storedHylaFaxID { // Adjust matching logic as needed.
					// Based on the notify result, create .done or .fail.
					if job.Result.Success {
						log.Printf("Notify indicates fax completed for job %s", synergyJobID)
						createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.done", storedHylaFaxID)), "")
					} else {
						log.Printf("Notify indicates fax failed for job %s", synergyJobID)
						createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.fail", storedHylaFaxID)), "")
					}
					// Remove job from queue since we've processed it.
					delete(jobQueue.entries, synergyJobID)
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
	go startFtp()
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

// -----------------------------
// SENDING & FTP WATCHER FUNCTIONS
// -----------------------------

func startFtp() {
	driver, err := file.NewDriver(os.Getenv("FTP_ROOT"))
	if err != nil {
		log.Fatal(err)
	}

	s, err := server.NewServer(&server.Options{
		Driver: driver,
		Auth: &server.SimpleAuth{
			Name:     os.Getenv("FTP_USER"),
			Password: os.Getenv("FTP_PASS"),
		},
		Perm:      server.NewSimplePerm("root", "root"),
		RateLimit: 1000000, // 1MB/s limit
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
	lines := strings.Split(string(content), "\n")
	if len(lines) < 2 {
		log.Printf("Invalid SFC file format: %s", filePath)
		return
	}
	faxNumber := lines[0]
	pdfFile := lines[1]
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
	err := createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.jobid", jobID)), hylaJobID)
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
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.fail", hylaJobID)), "")
		return "", err
	}
	defer resp.Body.Close()

	// Create a .sts file to indicate the fax has been sent.
	if err := createStsFile(hylaJobID, "6", "0", "0", "Sent to WebHook"); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("POST request failed with status: %s", resp.Status)
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.fail", hylaJobID)), "")
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
	addFaxJob(jobID, hylaJobID)
	log.Printf("Fax submitted successfully: FaxNumber=%s, PDFFile=%s, JobID=%s, Returned Job UUID=%s",
		faxNumber, pdfFile, jobID, outResp.JobUUID)

	return outResp.JobUUID, nil
}

func createFile(filePath, content string) error {
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", filePath, err)
	}
	log.Printf("File created: %s", filePath)
	return nil
}

func createStsFile(jobID, state, npages, totpages, status string) error {
	stsFilePath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("Q%s.sts", jobID))
	file, err := os.OpenFile(stsFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0660)
	if err != nil {
		return fmt.Errorf("error creating .sts file: %w", err)
	}
	defer file.Close()

	_, err = fmt.Fprintf(file, "state:%s\nnpages:%s\ntotpages:%s\nstatus:%s\n", state, npages, totpages, status)
	if err != nil {
		return fmt.Errorf("error writing to .sts file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("error syncing .sts file: %w", err)
	}
	log.Printf(".sts file created: %s", stsFilePath)
	return nil
}

func addFaxJob(synergyJobID, hylafaxJobID string) {
	jobQueue.Lock()
	defer jobQueue.Unlock()
	jobQueue.entries[synergyJobID] = hylafaxJobID
	log.Printf("Fax job added to queue: SynergyJobID=%s, HylaFaxJobID=%s", synergyJobID, hylafaxJobID)
}

func generateJobID() string {
	rand.Seed(time.Now().UnixNano())
	return fmt.Sprintf("%s%08d", JobIDPrefix, rand.Intn(100000000))
}

// (Optional) Monitor .done and .sts files to update the job queue if needed.
func monitorDoneFiles(dir string) {
	for {
		files, err := filepath.Glob(filepath.Join(dir, "*.done"))
		if err != nil {
			log.Printf("Error reading .done files: %v", err)
			continue
		}
		for _, file := range files {
			processDoneFile(file)
		}
		time.Sleep(5 * time.Second)
	}
}

func processDoneFile(filePath string) {
	fileName := filepath.Base(filePath)
	hylafaxJobID := strings.TrimSuffix(fileName, ".done")

	jobQueue.Lock()
	defer jobQueue.Unlock()
	for synergyJobID, storedHylaFaxID := range jobQueue.entries {
		if storedHylaFaxID == hylafaxJobID {
			log.Printf("Fax job completed: SynergyJobID=%s, HylaFaxJobID=%s", synergyJobID, hylafaxJobID)
			delete(jobQueue.entries, synergyJobID)
			break
		}
	}
	if err := os.Remove(filePath); err != nil {
		log.Printf("Error deleting .done file: %v", err)
	}
}

func monitorStatusFiles(dir string) {
	for {
		files, err := filepath.Glob(filepath.Join(dir, "*.sts"))
		if err != nil {
			log.Printf("Error reading .sts files: %v", err)
			continue
		}
		for _, file := range files {
			processStatusFile(file)
		}
		time.Sleep(5 * time.Second)
	}
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
