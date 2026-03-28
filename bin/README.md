# `bin/` — Compiled Binary Directory

This directory is the conventional output location for compiled Go binaries of the Intelligent Load Balancer project.

---

## Overview

While the project can be run directly from source using `go run ./cmd/loadbalancer/`, deploying to a staging or production environment requires compiling the application into a standalone executable binary. 

The `bin/` directory acts as the central location for these compiled artifacts.

### Why Use a `bin/` Directory?
1. **Clean Project Root:** Prevents cluttered root directories by isolating executable files.
2. **.gitignore Integration:** The `bin/` directory is standardized across Go projects and is usually added to `.gitignore`, ensuring compiled machine-code binaries are never accidentally checked into the Git repository.
3. **Consistent Release Processes:** CI/CD pipelines and deployment scripts have a predictable target directory to extract the release artifact from.

---

## Compiling the Load Balancer

To build the load balancer binary and place it in this directory, run the following command from the project root:

```bash
go build -o bin/lb ./cmd/loadbalancer/
```

- `-o bin/lb`: Specifies the output filepath and name (the binary will be named `lb` inside the `bin` folder).
- `./cmd/loadbalancer/`: The path to the `main` package containing the application entry point.

### Running the Compiled Binary

Once compiled, you can execute the binary directly:

```bash
./bin/lb
```

*Note:* Because the configuration loading paths in `main.go` are hardcoded relative to the working directory (e.g., `"config/config.json"` and `"web/dashboard.html"`), you should **always run the binary from the project root**, not by `cd`ing into the `bin/` folder first.

---

## Cross-Compilation

Go makes it incredibly easy to compile the load balancer for different operating systems and CPU architectures. You can place the various builds inside the `bin/` directory.

**Build for Linux (64-bit):**
```bash
GOOS=linux GOARCH=amd64 go build -o bin/lb-linux-amd64 ./cmd/loadbalancer/
```

**Build for macOS (Apple Silicon):**
```bash
GOOS=darwin GOARCH=arm64 go build -o bin/lb-darwin-arm64 ./cmd/loadbalancer/
```

**Build for Windows:**
```bash
GOOS=windows GOARCH=amd64 go build -o bin/lb-windows.exe ./cmd/loadbalancer/
```
