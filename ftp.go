package main

import (
	"bufio"
	"bytes"
	"fmt"
	"goftp.io/server/v2"
	"goftp.io/server/v2/driver/file"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	FaxDir      = "/synergyfaxq"
	JobIDPrefix = ""
)

type sfcFile struct {
	jobID     string
	sfcFile   string
	pdfFile   string
	faxNumber string
}

var cache = struct {
	sync.Mutex
	sfc map[string]sfcFile // Map of pdfFile -> .sfc content
	pdf map[string]string  // Map of filename -> path
}{sfc: make(map[string]sfcFile), pdf: make(map[string]string)}

var jobQueue = struct {
	sync.Mutex
	entries map[string]string // Map of synergyJobID -> hylafaxJobID
}{entries: make(map[string]string)}

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
				log.Printf("File created or modified: %s", event.Name)
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
	fileName := filepath.Base(filePath)

	switch ext {
	case ".sfc":
		handleSfcFile(filePath)
	case ".pdf":
		handlePdfFile(filePath)
	default:
		log.Printf("Unhandled file type: %s", fileName)
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

	// Check if the PDF file exists in the cache
	if _, exists := cache.pdf[pdfFile]; exists {
		submitFax(faxNumber, pdfFile, cache.pdf[pdfFile], filepath.Base(filePath))
		delete(cache.pdf, pdfFile) // Remove PDF from cache
	} else {
		// Cache the SFC file
		cache.sfc[pdfFile] = sfcFile{
			jobID:     strings.TrimSuffix(filePath, ".sfc"),
			sfcFile:   filePath,
			pdfFile:   pdfFile,
			faxNumber: faxNumber,
		}
	}
}

func handlePdfFile(filePath string) {
	fileName := filepath.Base(filePath)

	log.Printf("PDF file processed: %s", fileName)

	cache.Lock()
	defer cache.Unlock()

	// Check if the SFC file exists in the cache
	if content, exists := cache.sfc[fileName]; exists {
		// todo???
		submitFax(content.faxNumber, content.pdfFile, os.Getenv("FTP_ROOT")+FaxDir+"/"+content.pdfFile, content.jobID)
		delete(cache.sfc, fileName) // Remove SFC from cache
	} else {
		// Cache the PDF file
		cache.pdf[fileName] = filePath
	}
}

func submitFax(faxNumber, pdfFile, pdfPath, sfcFileName string) {
	jobID := strings.TrimSuffix(sfcFileName, ".sfc")

	hylaJobID := generateJobID()
	createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.jobid", jobID)), hylaJobID)

	// Open the PDF file
	fileData, err := os.ReadFile(pdfPath)
	if err != nil {
		log.Printf("Error reading PDF file: %v", err)
		return
	}

	// Construct the PUT request URL with query parameters
	putURL := fmt.Sprintf("%s?source=%s&destination=%s", os.Getenv("SEND_WEBHOOK_URL"), os.Getenv("FAX_NUMBER"), faxNumber)

	// Create a PUT request with the binary content of the PDF
	req, err := http.NewRequest("PUT", putURL, bytes.NewReader(fileData))
	if err != nil {
		log.Printf("Error creating PUT request: %v", err)
		return
	}

	// Set required headers (e.g., authorization, content type)
	req.Header.Set(os.Getenv("SEND_WEBHOOK_AUTH_HEADER"), os.Getenv("SEND_WEBHOOK_AUTH_TOKEN"))
	req.Header.Set("Content-Type", "application/pdf")

	// Send the PUT request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending PUT request: %v", err)
		return
	}
	defer resp.Body.Close()

	err = createStsFile(hylaJobID, "6", "0", "0", "Sent to WebHook")
	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("PUT request failed with status: %s", resp.Status)

		// Create .jobid and .done files
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.fail", hylaJobID)), "")
		return
	} else {
		// Create .jobid and .done files
		createFile(filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("%s.done", hylaJobID)), "")
	}

	log.Printf("Fax submitted successfully: FaxNumber=%s, PDFFile=%s, JobID=%s", faxNumber, pdfFile, jobID)
	// Add job to the queue
	// addFaxJob(jobID, hylaJobID)
}

func createFile(filePath, content string) error {
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", filePath, err)
	}
	log.Printf("File created: %s", filePath)
	return nil
}

// createStsFile creates a .sts file with the required status updates.
func createStsFile(jobID, state, npages, totpages, status string) error {
	// Define the .sts file path
	stsFilePath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, fmt.Sprintf("Q%s.sts", jobID))

	// Open or create the .sts file
	file, err := os.OpenFile(stsFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0660)
	if err != nil {
		return fmt.Errorf("error creating .sts file: %w", err)
	}
	defer file.Close()

	// Write the status parameters to the file
	_, err = fmt.Fprintf(file, "state:%s\nnpages:%s\ntotpages:%s\nstatus:%s\n", state, npages, totpages, status)
	if err != nil {
		return fmt.Errorf("error writing to .sts file: %w", err)
	}

	// Ensure all data is written to disk
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

	if scanner.Err() != nil {
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

func generateJobID() string {
	rand.Seed(time.Now().UnixNano())
	return fmt.Sprintf("%s%08d", JobIDPrefix, rand.Intn(100000000))
}
