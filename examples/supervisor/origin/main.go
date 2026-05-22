// origin is the vendor-side simulator for the supervisor demo. It owns an
// Ed25519 signing key, builds the example component on startup, signs the
// resulting tarball, and serves the file layout the supervisor expects:
//
//	GET /<component>/versions/required.txt   -> version string
//	GET /<component>/images/<v>_<os>_<arch>.tar.gz       -> archive
//	GET /<component>/images/<v>_<os>_<arch>.tar.gz.sig   -> detached signature
//
// The public key is written next to the key as update.pub so supervisor.yml
// can reference it.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	component := flag.String("component", "hello", "component name in URL paths")
	sourceDir := flag.String("source", "../hello", "path to the component module to build")
	version := flag.String("version", "v1", "version string")
	keyDir := flag.String("keys", "./keys", "directory holding update.key / update.pub")
	flag.Parse()

	priv, err := ensureKeys(*keyDir)
	if err != nil {
		log.Fatalf("origin: keys: %v", err)
	}

	archivePath, sigPath, err := buildAndSign(*sourceDir, *version, *component, priv)
	if err != nil {
		log.Fatalf("origin: build: %v", err)
	}
	log.Printf("origin: signed %s (sig=%s)", archivePath, sigPath)

	mux := http.NewServeMux()
	imagePrefix := "/" + *component + "/images/"
	versionsPath := "/" + *component + "/versions/required.txt"

	mux.HandleFunc(versionsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, *version)
	})

	mux.HandleFunc(imagePrefix, func(w http.ResponseWriter, r *http.Request) {
		// Serve only the archive + sig we generated for this run.
		base := strings.TrimPrefix(r.URL.Path, imagePrefix)
		switch base {
		case filepath.Base(archivePath):
			http.ServeFile(w, r, archivePath)
		case filepath.Base(sigPath):
			http.ServeFile(w, r, sigPath)
		default:
			http.NotFound(w, r)
		}
	})

	log.Printf("origin: listening on http://%s", *addr)
	log.Printf("  required.txt:   http://%s%s", *addr, versionsPath)
	log.Printf("  archive:        http://%s%s%s", *addr, imagePrefix, filepath.Base(archivePath))
	log.Printf("  signature:      http://%s%s%s", *addr, imagePrefix, filepath.Base(sigPath))

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("origin: serve: %v", err)
	}
}

func ensureKeys(dir string) (ed25519.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	privPath := filepath.Join(dir, "update.key")
	pubPath := filepath.Join(dir, "update.pub")

	if data, err := os.ReadFile(privPath); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("update.key is not PEM")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		priv, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("update.key is not ed25519")
		}
		return priv, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(privPath,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0600); err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pubPath,
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0644); err != nil {
		return nil, err
	}
	log.Printf("origin: generated keypair at %s / %s", privPath, pubPath)
	return priv, nil
}

func buildAndSign(sourceDir, version, component string, priv ed25519.PrivateKey) (string, string, error) {
	tmpBin, err := os.CreateTemp("", component+"-bin-*")
	if err != nil {
		return "", "", err
	}
	tmpBin.Close()
	defer os.Remove(tmpBin.Name())

	cmd := exec.Command("go", "build", "-o", tmpBin.Name(), ".")
	cmd.Dir = sourceDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("go build %s: %w", sourceDir, err)
	}

	body, err := os.ReadFile(tmpBin.Name())
	if err != nil {
		return "", "", err
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "bin/" + component,
		Mode:     0755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return "", "", err
	}
	if _, err := io.Copy(tw, bytes.NewReader(body)); err != nil {
		return "", "", err
	}
	if err := tw.Close(); err != nil {
		return "", "", err
	}
	if err := gz.Close(); err != nil {
		return "", "", err
	}

	archive := buf.Bytes()
	sig := ed25519.Sign(priv, archive)

	suffix := fmt.Sprintf("%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
	outDir, err := os.MkdirTemp("", "origin-images-")
	if err != nil {
		return "", "", err
	}
	archivePath := filepath.Join(outDir, suffix+".tar.gz")
	sigPath := archivePath + ".sig"
	if err := os.WriteFile(archivePath, archive, 0644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(sigPath, sig, 0644); err != nil {
		return "", "", err
	}
	return archivePath, sigPath, nil
}
