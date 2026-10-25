//go:build qualification

// runtime-qualification exercises the real launcher backend without Wails UI.
// It is never part of a public desktop build or a release authorization path.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"ligandx-launcher/internal/launcher"
)

type request struct {
	Action      string                       `json:"action"`
	Config      launcher.QualificationConfig `json:"config"`
	BundlePath  string                       `json:"bundle_path"`
	Version     string                       `json:"version"`
	EnvPath     string                       `json:"env_path"`
	LicensePath string                       `json:"license_path"`
	Groups      []string                     `json:"groups"`
	Upgrade     *upgradePaths                `json:"upgrade"`
}

type upgradePaths struct {
	Plan                 string `json:"plan"`
	Authorization        string `json:"authorization"`
	Signature            string `json:"signature"`
	PredecessorManifest  string `json:"predecessor_manifest"`
	PredecessorSignature string `json:"predecessor_signature"`
}

func readInput(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("qualification input exceeds size limit")
	}
	return data, nil
}

func run(input io.Reader, output io.Writer) error {
	start := time.Now()
	data, err := io.ReadAll(io.LimitReader(input, 2*1024*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("qualification request exceeds size limit")
	}
	var req request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return fmt.Errorf("trailing request data")
	}
	switch req.Action {
	case "install", "start", "configure", "status", "plan":
	default:
		return fmt.Errorf("unknown qualification action")
	}
	app, err := launcher.NewQualificationApp(context.Background(), req.Config)
	if err != nil {
		return err
	}
	defer app.Close()
	var intendedServices, intendedImages []string
	switch req.Action {
	case "install":
		var upgrade *launcher.QualificationUpgrade
		if req.Upgrade != nil {
			upgrade = &launcher.QualificationUpgrade{}
			for _, item := range []struct {
				path  string
				dest  *[]byte
				limit int64
			}{
				{req.Upgrade.Plan, &upgrade.Plan, 4 * 1024 * 1024}, {req.Upgrade.Authorization, &upgrade.Authorization, 64 * 1024},
				{req.Upgrade.Signature, &upgrade.Signature, 4096}, {req.Upgrade.PredecessorManifest, &upgrade.PredecessorManifest, 64 * 1024},
				{req.Upgrade.PredecessorSignature, &upgrade.PredecessorSignature, 4096},
			} {
				*item.dest, err = readInput(item.path, item.limit)
				if err != nil {
					return err
				}
			}
		}
		_, err = app.Install(req.BundlePath, req.Version, upgrade)
	case "configure":
		err = app.Configure(req.EnvPath, req.LicensePath, req.Groups)
	case "plan":
		// Read-only: the services and images a start of these groups needs.
		intendedServices, err = app.ServicePlan(req.Groups)
		if err == nil {
			intendedImages, err = app.ImagePlan(req.Groups)
		}
	case "start":
		intendedServices, err = app.ServicePlan(req.Groups)
		if err == nil {
			err = app.Start(req.Groups)
		}
	}
	result := map[string]interface{}{"schema": "ligandx-launcher-qualification/1", "qualification_only": true, "action": req.Action, "ok": err == nil, "status": app.Status(), "events": app.EventCounts(), "elapsed_seconds": time.Since(start).Seconds()}
	if intendedServices != nil {
		result["requested_services"] = intendedServices
	}
	if intendedImages != nil {
		result["requested_images"] = intendedImages
	}
	// Never serialize request contents: environment and license inputs are private.
	if err != nil {
		result["error"] = err.Error()
	}
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
