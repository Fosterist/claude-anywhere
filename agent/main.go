// Command agent runs on whatever machine holds the actual project folders
// (your PC). It polls the bot for pending jobs, executes `claude -p` locally
// in the right directory, and posts the result back.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/Fosterist/claude-anywhere/internal/api"
	"github.com/Fosterist/claude-anywhere/internal/config"
)

func main() {
	botURL := mustEnv("BOT_URL")
	agentToken := mustEnv("AGENT_TOKEN")
	configPath := envOr("CONFIG_PATH", "projects.json")
	claudeBin := envOr("CLAUDE_BIN", "claude")
	pollInterval := 3 * time.Second

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	client := &http.Client{Timeout: 30 * time.Minute} // claude runs can take a while

	log.Printf("agent watching %s (poll every %s)", botURL, pollInterval)
	for {
		job, err := fetchNext(client, botURL, agentToken)
		if err != nil {
			log.Printf("poll error: %v", err)
			time.Sleep(pollInterval)
			continue
		}
		if job == nil {
			time.Sleep(pollInterval)
			continue
		}

		log.Printf("job #%d: project=%s", job.ID, job.Project)
		res := runJob(claudeBin, cfg, *job)
		if err := postResult(client, botURL, agentToken, res); err != nil {
			log.Printf("failed to post result for job #%d: %v", job.ID, err)
		}
	}
}

func fetchNext(client *http.Client, botURL, token string) (*api.Job, error) {
	req, _ := http.NewRequest("GET", botURL+"/jobs/next", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var job api.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func postResult(client *http.Client, botURL, token string, res api.Result) error {
	body, _ := json.Marshal(res)
	req, _ := http.NewRequest("POST", botURL+"/jobs/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bot responded %d", resp.StatusCode)
	}
	return nil
}

// claudeResult mirrors the fields we care about from `claude --output-format json`.
type claudeResult struct {
	Result      string  `json:"result"`
	SessionID   string  `json:"session_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError     bool    `json:"is_error"`
	Subtype     string  `json:"subtype"`
}

func runJob(claudeBin string, cfg *config.Config, job api.Job) api.Result {
	dir, ok := cfg.Projects[job.Project]
	if !ok {
		return api.Result{JobID: job.ID, IsError: true, ErrorText: "unknown project: " + job.Project}
	}

	args := []string{"-p", job.Prompt, "--output-format", "json"}
	if job.Permission != "" {
		args = append(args, "--permission-mode", job.Permission)
	}
	if job.SessionID != "" {
		args = append(args, "--resume", job.SessionID)
	}
	if job.MaxBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", job.MaxBudget))
	}

	cmd := exec.Command(claudeBin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return api.Result{
			JobID:     job.ID,
			IsError:   true,
			ErrorText: fmt.Sprintf("claude exited with error: %v\nstderr: %s", err, stderr.String()),
		}
	}

	var cr claudeResult
	if err := json.Unmarshal(stdout.Bytes(), &cr); err != nil {
		return api.Result{
			JobID:     job.ID,
			IsError:   true,
			ErrorText: fmt.Sprintf("could not parse claude output: %v\nraw: %s", err, stdout.String()),
		}
	}

	return api.Result{
		JobID:     job.ID,
		Result:    cr.Result,
		SessionID: cr.SessionID,
		CostUSD:   cr.TotalCostUSD,
		IsError:   cr.IsError,
		ErrorText: cr.Subtype,
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required env var %s", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
