# Synergy Matters Fax Server

Synergy Matters Fax Server is designed to facilitate fax sending with a robust architecture that comprises a main fax service and an integrated SFTP server (SFTPGo) for managing fax file transfers.

## Architecture Overview

- **Main Fax Service:** Runs as a systemd service on the host. It handles fax processing and webhook integrations.
- **SFTP Server (SFTPGo):** Managed via Docker Compose, providing FTP/SFTP access for fax file storage and transfers.

## Prerequisites

- A Linux system with systemd support.
- [Go](https://golang.org/dl/) (if building directly) or Docker (if using Docker for building).
- Docker and Docker Compose (for the SFTPGo service).
- Ensure required ports are open:
  - **Main Fax Service: 8080** (configurable via `PORT` in `.env`)
  - SFTPGo Web Interface: 8081
  - FTP: 21
  - Passive Ports: 50000-50100

## Installation

### 1. Clone the Repository
```bash
git clone https://github.com/sagostin/synergymattersfax.git
cd synergymattersfax
```

### 2. Build the Main Fax Service

You have two options to build the main service binary:

#### Option A: Build on the Host
Ensure Go (v1.24.1 or later) is installed and run:
```bash
go build -o synergymatters_fax .
```
Then move the binary to your deployment directory (e.g., `/home/ubuntu/synergymattersfax`).

#### Option B: Build via Docker
Run the build script:
```bash
sudo ./build.sh
```
Extract the built binary, rename it to `synergymatters_fax`, and place it in `/home/ubuntu/synergymattersfax`.

### 3. Configure Environment Variables

Create a `.env` file in the repository root (or in your deployment directory) with the following content. Adjust the values as needed:
```env
FTP_ROOT=./synergyfax_ftp
FAX_NUMBER=TEN_DIGIT_NUMBER_HERE
SEND_WEBHOOK_URL=http://YOUR_FAX_SERVER_URL:8080/fax/send
SEND_WEBHOOK_USERNAME=YOUR_USERNAME_HERE
SEND_WEBHOOK_PASSWORD=YOUR_PASSWORD_HERE
PORT=8080
SUMATRA_PDF_PATH=./SumatraPDF.exe   # Windows-only; ignored on Linux
PRINT_ONLY_MODE=false               # Windows-only; harmless no-op on Linux
FAX_RETENTION_HOURS=24              # 0 disables cleanup; received PDFs accumulate forever otherwise
FAX_CLEANUP_INTERVAL_MINUTES=60     # How often the cleanup sweeper runs
```

**Understanding the PORT setting:**
- The server listens on port **8080 by default**.
- To change the port, edit `PORT=8080` in your `.env` file (e.g., `PORT=9090`).
- If you omit the `PORT` line entirely, the server will still default to 8080.

### 4. Install and Start the Systemd Service

Copy the provided systemd service file to `/etc/systemd/system/`:
```bash
sudo cp synergymattersfax.service /etc/systemd/system/
```

Edit the service file (`/etc/systemd/system/synergymattersfax.service`) if necessary to ensure the paths are correct:
- `WorkingDirectory` should point to the directory containing the binary (e.g., `/home/ubuntu/synergymattersfax`).
- `ExecStart` should point to the full path of the `synergymatters_fax` binary.

Reload systemd and start the service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable synergymattersfax
sudo systemctl start synergymattersfax
```

### 5. Start the SFTPGo Service with Docker Compose

The SFTPGo service is managed via Docker Compose. Ensure Docker is installed and running, then execute:
```bash
sudo docker compose up -d
```
This will launch SFTPGo with the following port mappings:
- Web Interface: `http://<SERVER_IP>:8081`
- FTP: Port 21
- Passive Ports: 50000-50100

On first access, configure the SFTPGo admin user and then create additional users as needed. Make sure to set each user’s root directory to `/srv/sftpgo/synergyfax_ftp`.

## Windows Standalone Deployment

On Windows, you can run the fax service without Docker/SFTPGo. The FTP portion is optional and only needed for integration with other software.

### Building for Windows

Cross-compile from Linux/Mac:
```bash
GOOS=windows GOARCH=amd64 go build -o synergymatters_fax.exe .
```

Or build directly on Windows with Go installed:
```bash
go build -o synergymatters_fax.exe .
```

### Configuration

Create a `.env` file in your deployment directory:

```env
FTP_ROOT=C:\FaxStorage
FAX_NUMBER=TEN_DIGIT_NUMBER_HERE
SEND_WEBHOOK_URL=http://YOUR_FAX_SERVER_URL:8080/fax/send
SEND_WEBHOOK_USERNAME=YOUR_USERNAME_HERE
SEND_WEBHOOK_PASSWORD=YOUR_PASSWORD_HERE
PORT=8080
SUMATRA_PDF_PATH=C:\Path\To\SumatraPDF.exe
PRINT_ONLY_MODE=false
PRINTER_NAME=Your Printer Name Here
FAX_RETENTION_HOURS=24
FAX_CLEANUP_INTERVAL_MINUTES=60
```

**Understanding the PORT setting:**
- The server listens on port **8080 by default**.
- To change the port, edit `PORT=8080` in your `.env` file (e.g., `PORT=9090`).
- If you omit the `PORT` line entirely, the server will still default to 8080.

**Path Simplification:** To save PDFs directly to `FTP_ROOT` without a subdirectory, edit `main.go` and change line 29:

```go
FaxDir      = ""  // Set to "" to save directly to FTP_ROOT
```

With `FaxDir = ""`, faxes will be saved to `C:\FaxStorage\{UUID}{timestamp}.pdf` instead of `C:\FaxStorage\synergyfaxq\{UUID}{timestamp}.pdf`.

### Running as a Windows Service with Servy

[Servy](https://github.com/aelassas/servy) is a modern Windows service
wrapper that bundles a GUI app, a CLI (`servy-cli`), and a PowerShell
module. It is a drop-in alternative to NSSM with better logging, health
checks, and process monitoring.

1. Install Servy (any one of these):
   ```cmd
   winget install servy
   :: or
   choco install -y servy
   :: or
   scoop install servy
   ```
   Or download the latest release from
   <https://github.com/aelassas/servy/releases/latest>.
2. Open the Servy desktop app, **or** use the CLI from an **elevated**
   Command Prompt / PowerShell:
   ```cmd
   servy-cli install ^
     --name="SynergyFax" ^
     --path="C:\FaxService\synergymatters_fax.exe" ^
     --startupDir="C:\FaxService"
   ```
   The startup directory is what becomes the working directory for the
   wrapped process — Servy picks up `.env` from that folder.
3. In the Servy UI (or via `servy-cli set ...`), confirm:
   - **Startup type:** Automatic
   - **Working directory / startup dir:** `C:\FaxService`
   - Environment variables are read from `.env` in the startup
     directory automatically — no extra config needed.
4. Start the service:
   ```cmd
   servy-cli start --name="SynergyFax"
   :: or, from an elevated prompt:
   sc.exe start SynergyFax
   ```

See the [Servy wiki](https://github.com/aelassas/servy/wiki) for full
options (process priority, log rotation, pre/post-launch hooks, etc.).

### Printer Integration

Since printers can be configured to monitor a folder for files to print:

1. Set `FTP_ROOT` to a network share path accessible by both the service and printer
2. Configure your printer to monitor this folder
3. When faxes arrive, they are saved as PDFs directly to this location and are immediately available for printing

Common network share formats:
- `\\SERVERNAME\FaxShare`
- `\\192.168.1.100\FaxShare`

#### `.recv` file format

For each received fax the service writes a HylaFax-compatible `.recv`
file next to the PDF in `FTP_ROOT` (or `FTP_ROOT\synergyfaxq\` if the
default `FaxDir` is in use). The file is plain text, four lines:

```
MM/DD/YY HH:MM       <- receive time, America/Vancouver timezone
ttyS0                <- hardcoded device identifier
{baseName}timestamp  <- PDF base name (without .pdf extension)
caller ID number     <- CIDNum from the inbound webhook payload
```

Downstream tools that already consume HylaFax `.recv` files can pick
this up unchanged. The file is **not** written when
`PRINT_ONLY_MODE=true`.

The `synergyfaxq` subdirectory is auto-created on first receive via
`os.MkdirAll` — if you point an SFTPGo user root at `FTP_ROOT` you do
not need to pre-create it.

### PRINT_ONLY_MODE (Windows Direct Printing)

> ⚠️ **PRINT_ONLY_MODE = receive-only.** When this flag is `true`, the
> background folder watcher that drives outbound faxing (`.sfc` files
> dropped into `FTP_ROOT/synergyfaxq/`) is **not started**. Faxes can
> still be **received** via `POST /fax-receive`, but **sending** via
> the HylaFax-style `.sfc` handoff will silently no-op — the `.sfc`
> file will sit in the directory forever, nothing will be submitted to
> `SEND_WEBHOOK_URL`, and no error will be logged. If you need
> outbound faxing on Windows, leave this `false` (you can still pair
> it with a printer's folder-monitor against `FTP_ROOT`, or just print
> the PDFs out-of-band).

`PRINT_ONLY_MODE` switches the **Windows** build from the standard
`.recv`-file handoff to a direct-to-printer workflow. By default
(`false`), every received fax writes a `{name}.recv` metadata file
alongside the PDF in `FTP_ROOT` — this is what triggers downstream
fax-receipt integration (status emails, archiving, etc.). When
`PRINT_ONLY_MODE=true`, that `.recv` file is **not** written; instead
the service hands the PDF straight to a Windows printer via SumatraPDF.

> **Note:** even with `PRINT_ONLY_MODE=true`, the PDF is **still
> written to disk first** (`FTP_ROOT/synergyfaxq/` by default), and
> SumatraPDF prints it from that path. `FTP_ROOT` must still point at
> a writable directory on the fax server — the service does not have
> a "print-only, no disk" mode. If `FTP_ROOT` is a network share or
> path you wanted to avoid, you'll need to either accept the local
> write or modify `main.go` to print from a buffer instead.
>
> Disk usage is bounded by `FAX_RETENTION_HOURS` — see the
> [Fax Retention / Cleanup](#fax-retention--cleanup) section.

#### When to use it

- You're deploying **Windows standalone** (no SFTPGo, no shared FTP
  folder, no downstream integration that consumes the `.recv` file).
- You want received faxes to print on a local/network printer
  immediately, with no folder-monitor polling delay.
- Your printer is reachable from the fax server by name (e.g.
  `\\PRINTSERVER\Office-Laserjet` or whatever shows up in
  `Printers & Scanners`).

If instead you point `FTP_ROOT` at a network share and let your
printer's folder-monitor pick up the PDFs (see "Printer Integration"
above), leave `PRINT_ONLY_MODE=false` — the `.recv` file is harmless
and the direct-print path is redundant.

#### Required configuration

With `PRINT_ONLY_MODE=true` you also need:

| Variable          | Required | Notes                                                      |
| ----------------- | -------- | ---------------------------------------------------------- |
| `PRINTER_NAME`    | Yes      | Exact Windows printer name (e.g. `HP LaserJet Pro M404`).  |
| `SUMATRA_PDF_PATH`| Yes*     | Path to `SumatraPDF.exe` (defaults to `.\SumatraPDF.exe`). |
| `FTP_ROOT`        | Yes      | Where the PDF is saved before printing.                    |

`*` Strongly recommended to set explicitly — see the SumatraPDF
section below.

The PDF is printed with `-print-settings simplex,fit,monochrome`, so
every fax lands as a single black-and-white page fitted to the print
area, regardless of how the original PDF was authored.

#### Linux behavior

On Linux, `PRINT_ONLY_MODE` is a harmless no-op: the underlying print
helper shells out to `powershell` (Windows-only), the call fails, the
failure is logged, and the service continues. Leave it `false` on
Linux — Linux deployments rely on the `.recv` file flow.

### SumatraPDF (Windows Printing)

The Windows service uses SumatraPDF to silently print received faxes
(`-print-settings simplex,fit,monochrome -print-to "<printer>"`).

#### Quick install (portable)

1. Download the **portable** version from
   <https://www.sumatrapdfreader.org/download-free-pdf-viewer>
   (look for the small "Portable" download — the 64-bit installer is
   not what you want).
2. Extract the zip. Inside you will find `SumatraPDF.exe` (along with
   a handful of supporting DLLs and translation files — keep them all
   together).
3. Either:
   - **Drop it in the service's working directory** (the startup dir you
     set in Servy, e.g. `C:\FaxService`). Then no `SUMATRA_PDF_PATH`
     configuration is needed — the service will find `.\SumatraPDF.exe`
     automatically.
   - **Or** place it anywhere you like (e.g. `C:\Tools\SumatraPDF\`)
     and set `SUMATRA_PDF_PATH` in `.env` to the absolute path of
     `SumatraPDF.exe`.

#### Why this matters under Servy

If `SUMATRA_PDF_PATH` is unset, the service falls back to
`.\SumatraPDF.exe` — resolved relative to the *current working
directory* of the running process. Under Servy that is whatever you set
as `--startupDir` (or the "Working directory" in the GUI), which is
often *not* where you unpacked SumatraPDF. Setting `SUMATRA_PDF_PATH`
to an absolute path — or simply dropping `SumatraPDF.exe` into that
startup directory — avoids silent print failures.

Linux ignores this variable entirely.

### Managing the Service

```cmd
servy-cli start   --name="SynergyFax"    :: Start the service
servy-cli stop    --name="SynergyFax"    :: Stop the service
servy-cli restart --name="SynergyFax"    :: Restart the service
servy-cli status  --name="SynergyFax"    :: Check service status
sc.exe start  SynergyFax                  :: Start (any elevated prompt)
sc.exe stop   SynergyFax                  :: Stop  (any elevated prompt)
sc.exe query  SynergyFax                  :: Status (any elevated prompt)
```

You can also manage the service visually through the **Servy Manager**
app (ships with Servy) — it shows live CPU/RAM graphs, stdout/stderr
preview, and lets you browse rotated log files.

View logs via Windows Event Viewer, the Servy Manager app, or by
configuring stdout/stderr redirection in Servy.

## Outbound Sending (`.sfc` Handoff)

When `PRINT_ONLY_MODE` is `false` (default — the only mode where this
is wired up), the service runs a background watcher over
`FTP_ROOT/synergyfaxq/` (or `FTP_ROOT/` if you've changed `FaxDir` to
`""`). It implements a HylaFax-compatible send handoff:

1. A client drops two files into the watched directory:
   - `{jobID}.sfc` — a 2-line text file: line 1 is the destination
     fax number, line 2 is the PDF filename to send.
   - `{pdfFile}.pdf` — the PDF document to fax.
2. The watcher picks the `.sfc` up, writes a `{jobID}.jobid` file
   containing a Hylafax-style numeric job ID, and POSTs the PDF (as
   multipart form data) to `SEND_WEBHOOK_URL` with HTTP Basic Auth
   using `SEND_WEBHOOK_USERNAME` / `SEND_WEBHOOK_PASSWORD`.
   `FAX_NUMBER` is sent as the caller ID.
3. The upstream fax service is expected to call back to
   `POST /fax-notify` with the job's eventual status.
4. On success the service writes `q{jobID}.sts` and `q{jobID}.done`,
   and removes the `.sfc` and `.pdf`. On failure it writes
   `q{jobID}.fail` and removes the input files.

### Required env vars for outbound

| Variable                | Required | Notes                                             |
| ----------------------- | -------- | ------------------------------------------------- |
| `FTP_ROOT`              | Yes      | Where `.sfc`/`.pdf` files are dropped & status files are written. |
| `FAX_NUMBER`            | Yes      | Caller-ID sent as `caller_number` on outbound.    |
| `SEND_WEBHOOK_URL`      | Yes      | The upstream fax service endpoint (e.g. `http://provider.example/fax/send`). |
| `SEND_WEBHOOK_USERNAME` | Yes      | HTTP Basic Auth username.                         |
| `SEND_WEBHOOK_PASSWORD` | Yes      | HTTP Basic Auth password.                         |
| `PRINT_ONLY_MODE`       | Yes (must be `false`) | Disables this entire watcher.               |

### Status file lifecycle

| File                  | When written                                | Content            |
| --------------------- | ------------------------------------------- | ------------------ |
| `{jobID}.jobid`       | After `.sfc` is consumed                    | Hylafax job ID + `\r` |
| `q{jobID}.sts`        | After upstream POST returns / on notify     | Status code + text |
| `q{jobID}.done`       | On successful completion (notify confirms)  | `\r`               |
| `q{jobID}.fail`       | On upstream POST failure / failed notify    | `\r`               |

If you want outbound sending on Windows, leave `PRINT_ONLY_MODE=false`
— see the warning at the top of the **PRINT_ONLY_MODE** section.

## HTTP API

The service listens on `PORT` (default `8080`) and exposes two
endpoints. Both expect `Content-Type: application/json`. Webhook
authentication (where used) is HTTP Basic Auth via
`SEND_WEBHOOK_USERNAME` / `SEND_WEBHOOK_PASSWORD`.

### `POST /fax-receive`

Inbound webhook called by the upstream fax service when a fax is
received. The request body is a JSON `FaxReceive` payload (base64-
encoded PDF in the `file_data` field, plus metadata: `uuid`, `cidnum`,
`filename`, etc.).

Behavior:
- Saves the PDF to `FTP_ROOT/synergyfaxq/{baseName}{timestamp}.pdf`
  (auto-creates the subdirectory).
- If `PRINT_ONLY_MODE=false`: also writes a `.recv` metadata file
  alongside the PDF (see [`.recv` file format](#recv-file-format)).
- If `PRINT_ONLY_MODE=true`: prints the PDF via SumatraPDF instead
  of writing `.recv`.
- Returns `200 OK` on success, `4xx`/`5xx` on validation or I/O errors.

### `POST /fax-notify`

Inbound webhook called by the upstream fax service when an outbound
fax's status changes. The request body is a JSON `WebhookPayload`
containing a `fax_job_results.results` map (keyed by job UUID, with
`status`, `result.success`, etc.) and a top-level `fax_job.calluuid`.

Behavior:
- Updates the in-memory job record (`LastStatus`, `LastUpdatedAt`).
- On success: writes `q{jobID}.sts` + `q{jobID}.done`, removes the
  original `.sfc` and `.pdf`.
- On failure: writes `q{jobID}.sts` + `q{jobID}.fail`, removes the
  original `.sfc` and `.pdf`.

This endpoint is a **no-op** when `PRINT_ONLY_MODE=true` because the
outbound watcher that creates the job records isn't running.

## Fax Retention / Cleanup

A background sweeper periodically removes old **received** faxes from
`FTP_ROOT/synergyfaxq/` (or `FTP_ROOT/` if you've set `FaxDir=""`).
This applies to both `.pdf` and `.recv` files. **Outbound** files
(`.sfc`, `q*.sts`, `q*.done`, `q*.fail`, `.jobid`) are not touched —
their lifecycle is managed separately by the watcher.

### Configuration

| Variable                     | Default | Notes                                                              |
| ---------------------------- | ------- | ------------------------------------------------------------------ |
| `FAX_RETENTION_HOURS`        | `24`    | Files older than this are deleted. Set to `0` to disable entirely. |
| `FAX_CLEANUP_INTERVAL_MINUTES` | `60`  | How often the sweeper runs (and once on startup).                  |

### How the timer relates to printing

The sweeper uses each file's modification time (`mtime`):

- **`PRINT_ONLY_MODE=false`** — file mtime is the **receipt time**
  (when the PDF was written). Retention is "keep for 24h after
  receipt".
- **`PRINT_ONLY_MODE=true`** — after a **successful** SumatraPDF
  print, the service updates the file's mtime to the print time via
  `os.Chtimes`. Retention is therefore "keep for 24h after print".
  - **Failed prints keep the original mtime** and survive for the
    full retention window, giving you a longer tail to debug/retry
    from disk before they're deleted.

### Notes & caveats

- Locked files (currently being printed, or being read by a downstream
  tool that opened the `.recv`) fail to delete silently and are
  retried on the next sweep — there's no separate retry queue.
- `FAX_RETENTION_HOURS=0` disables the sweeper entirely; received
  PDFs will then accumulate forever (a warning is logged on startup).
- The sweeper does not look at the `.recv` file's content — it only
  uses mtime. If you manually touch/rewrite a file and want it
  retained, set its mtime back manually.
- The retention clock is wall-clock based, not based on the receipt
  notification — clock skew on the fax server affects deletion timing.

## Accessing the Services

- **Main Fax Service:** `http://<SERVER_IP>:8080` (default port, configurable via `PORT` env var)
- **SFTPGo Web Interface:** Accessible at `http://<SERVER_IP>:8081`
- **FTP:** Port 21 (for SFTPGo file transfers)

## Troubleshooting

- Verify that your `.env` file is correctly configured.
- For systemd service logs, run:
  ```bash
  sudo journalctl -u synergymattersfax -f
  ```
- For Docker Compose logs:
  ```bash
  sudo docker compose logs
  ```

## License

Refer to the [LICENSE](LICENSE) file for details.
