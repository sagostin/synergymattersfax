package main

import (
	"encoding/base64"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/kataras/iris/v12"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"time"
)

// WebhookPayload defines the incoming JSON structure.
type WebhookPayload struct {
	FaxJobResults FaxJobResults `json:"fax_job_results"`
	FileData      string        `json:"file_data"`
}

// FaxJobResults holds the map of individual results and the overall fax job.
type FaxJobResults struct {
	Results map[string]FaxJob `json:"results"`
	FaxJob  FaxJob            `json:"fax_job"`
}

// FaxJob defines the structure for each fax job.
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

// FaxReceive represents the JSON payload for a received fax.
type FaxReceive struct {
	UUID        string `json:"uuid"`
	CallUUID    string `json:"call_uuid"`
	SrcTenantID int    `json:"src_tenant_id"`
	DstTenantID int    `json:"dst_tenant_id"`
	Number      string `json:"number"`
	CIDNum      string `json:"cidnum"`
	CIDName     string `json:"cidname"`
	Filename    string `json:"filename"`
	Ident       string `json:"ident"`
	Header      string `json:"header"`
	// Endpoints and result are omitted here for brevity.
	Result        FaxResult     `json:"result"`
	FaxSourceInfo FaxSourceInfo `json:"fax_source_info"`
	Status        string        `json:"status"`
	TotDials      int           `json:"totdials"`
	NDials        int           `json:"ndials"`
	TotTries      int           `json:"tottries"`
	Ts            string        `json:"ts"`
	FileData      string        `json:"file_data"`
}

// Endpoint represents a destination endpoint for the fax.
type Endpoint struct {
	ID           int    `json:"id"`
	Type         string `json:"type"`
	TypeID       int    `json:"type_id"`
	EndpointType string `json:"endpoint_type"`
	Endpoint     string `json:"endpoint"`
	Priority     int    `json:"priority"`
}

// FaxResult holds the result details for a fax job.
type FaxResult struct {
	UUID       string `json:"uuid"`
	StartTs    string `json:"start_ts"`
	EndTs      string `json:"end_ts"`
	Success    bool   `json:"success"`
	ResultCode int    `json:"result_code"` // SpanDSP
	ResultText string `json:"result_text"`
	// Additional fields such as Hangupcause and ResultText can be added here.
}

// FaxSourceInfo contains source details for the fax.
type FaxSourceInfo struct {
	Timestamp  string `json:"timestamp"`
	SourceType string `json:"source_type"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id"`
}

func main() {
	err := godotenv.Load()
	if err != nil {
		return
	}
	go startFtp()
	go watchFaxFolder(os.Getenv("FTP_ROOT") + FaxDir)
	// go monitorDoneFiles(os.Getenv("FTP_ROOT") + FaxDir)
	// go monitorStatusFiles(os.Getenv("FTP_ROOT") + FaxDir)

	//go startPop3()

	app := iris.New()

	// Endpoint for receiving fax data.
	app.Post("/fax-receive", func(ctx iris.Context) {
		var fax FaxReceive
		if err := ctx.ReadJSON(&fax); err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": err.Error()})
			return
		}

		// Decode the file_data (assumed to be a base64-encoded PDF).
		pdfBytes, err := base64.StdEncoding.DecodeString(fax.FileData)
		if err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": "failed to decode file_data: " + err.Error()})
			return
		}

		// Save the PDF to a local file (the filename is provided in the payload).
		pdfLocalPath := filepath.Join("local_files", filepath.Base(fax.Filename))
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

		// --- Create the .recv file ---
		// In a real scenario, the hylafax job id may be generated or obtained elsewhere.
		// Here we simulate it using the UUID (or you could generate a sequence).
		hylafaxJobID := "fax000000001" // Example job id; in production, generate or lookup properly.
		recvFilename := hylafaxJobID + ".recv"
		recvLocalPath := filepath.Join(os.Getenv("FTP_ROOT")+FaxDir, recvFilename)

		// Build the 4-line content:
		// 1. Date/time – using current time in a sample format.
		// 2. TTY info – here we simulate with "ttyS1"
		// 3. PDF filename (base name)
		// 4. Fax number from the payload.
		recvContent := fmt.Sprintf("%s\n%s\n%s\n%s\n",
			time.Now().Format("01/02/06 15:04"),
			"ttyS1", // In production, determine which port (ttyS1 or ttyACM1 etc.)
			filepath.Base(fax.Filename),
			fax.Number,
		)
		if err := ioutil.WriteFile(recvLocalPath, []byte(recvContent), 0644); err != nil {
			ctx.StatusCode(iris.StatusInternalServerError)
			ctx.JSON(iris.Map{"error": "failed to write recv file: " + err.Error()})
			return
		}
		log.Printf("Created recv file: %s", recvLocalPath)

		// In a complete system you would now also:
		//  - Create and upload the jobid.sfc file when sending a fax.
		//  - Monitor *.jobid, *.sts, *.done, and *.fail files on the FTP server.
		//  - Process status updates accordingly.
		// For brevity, these additional steps are not shown here.

		ctx.JSON(iris.Map{
			"message": "Fax received and files uploaded successfully",
			"pdf":     filepath.Base(pdfLocalPath),
			"recv":    filepath.Base(recvLocalPath),
		})
	})

	// Define a POST endpoint to receive the webhook request.
	app.Post("/notify", func(ctx iris.Context) {
		var payload WebhookPayload

		// Read and unmarshal the JSON body.
		if err := ctx.ReadJSON(&payload); err != nil {
			ctx.StatusCode(iris.StatusBadRequest)
			ctx.JSON(iris.Map{"error": err.Error()})
			return
		}

		var successfulStatuses []string

		// Iterate through individual fax job results.
		for key, job := range payload.FaxJobResults.Results {
			if job.Result.Success {
				successfulStatuses = append(successfulStatuses, fmt.Sprintf("Job %s status: %s", key, job.Status))
			}
		}

		// Also check the overall fax job result.
		if payload.FaxJobResults.FaxJob.Result.Success {
			successfulStatuses = append(successfulStatuses, fmt.Sprintf("Overall FaxJob status: %s", payload.FaxJobResults.FaxJob.Status))
		}

		// Optionally, you can log the successful statuses.
		log.Printf("Successful statuses: %v", successfulStatuses)

		// Return the successful statuses as JSON.
		ctx.JSON(iris.Map{
			"successful_statuses": successfulStatuses,
		})
	})

	// Start the Iris web server on port 8080.
	app.Listen(":8080")

	select {} // Block forever
}
