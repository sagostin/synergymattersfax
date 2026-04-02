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
```

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
```

**Path Simplification:** To save PDFs directly to `FTP_ROOT` without a subdirectory, edit `main.go` and change line 29:

```go
FaxDir      = ""  // Set to "" to save directly to FTP_ROOT
```

With `FaxDir = ""`, faxes will be saved to `C:\FaxStorage\{UUID}{timestamp}.pdf` instead of `C:\FaxStorage\synergyfaxq\{UUID}{timestamp}.pdf`.

### Running as a Windows Service with NSSM

1. Download NSSM from https://nssm.cc/download
2. Extract and note the path to `nssm.exe`
3. Install the service:
   ```cmd
   nssm install SynergyFax "C:\path\to\synergymatters_fax.exe"
   ```
4. Configure the service:
   - **Startup type:** Automatic
   - **Working directory:** Your deployment folder (e.g., `C:\FaxService`)
5. Set environment variables by editing the service or creating a `.env` file in the working directory
6. Start the service:
   ```cmd
   nssm start SynergyFax
   ```

### Printer Integration

Since printers can be configured to monitor a folder for files to print:

1. Set `FTP_ROOT` to a network share path accessible by both the service and printer
2. Configure your printer to monitor this folder
3. When faxes arrive, they are saved as PDFs directly to this location and are immediately available for printing

Common network share formats:
- `\\SERVERNAME\FaxShare`
- `\\192.168.1.100\FaxShare`

### Managing the Service

```cmd
nssm start SynergyFax    # Start the service
nssm stop SynergyFax     # Stop the service
nssm restart SynergyFax  # Restart the service
nssm status SynergyFax   # Check service status
nssm edit SynergyFax     # Edit service configuration
```

View logs via Windows Event Viewer or redirect output in NSSM configuration.

## Accessing the Services

- **SFTPGo Web Interface:** Accessible at `http://<SERVER_IP>:8081`
- **Main Fax Service:** Operates as a backend service handling fax processing and webhook communications.

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
