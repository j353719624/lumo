// Process supervision: the console spawns one engine subprocess per enabled
// connection config, captures its logs and polls its localhost status
// endpoint. A crashed engine is restarted with exponential backoff; a crash
// in one connection therefore never takes down the others.
package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/log"
)

const (
	engineRestartMin = 2 * time.Second
	engineRestartMax = 30 * time.Second
	engineStableFor  = 60 * time.Second // alive this long → backoff resets
)

var engineLogRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? \[(\w+)\] ?(.*)$`)

// lineWriter funnels engine subprocess output into the per-connection log
// ring, stripping the engine's own timestamp/level and re-emitting through
// the instance (which adds the connection name prefix).
type lineWriter struct {
	logi *log.Instance
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line == "" {
			continue
		}
		if m := engineLogRe.FindStringSubmatch(line); m != nil {
			msg := m[2]
			switch strings.ToUpper(strings.TrimSpace(m[1])) {
			case "DEBUG":
				w.logi.Debugf("%s", msg)
			case "WARN":
				w.logi.Warnf("%s", msg)
			case "ERROR":
				w.logi.Errorf("%s", msg)
			default:
				w.logi.Infof("%s", msg)
			}
			continue
		}
		w.logi.Infof("%s", line)
	}
	return len(p), nil
}

// spawn launches one engine subprocess and returns the command. A watchdog
// terminates it when the supervision context is cancelled.
func (r *run) spawn(ctx context.Context) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "engine", "-c", r.tomlPath, "--admin", r.admin)
	cmd.Stdout = &lineWriter{logi: r.logi}
	cmd.Stderr = &lineWriter{logi: r.logi}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		r.mu.Lock()
		cmd := r.cmd
		r.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}()
	return cmd, nil
}

// supervise keeps an engine running until the run is stopped: restarts on
// unexpected exits with exponential backoff that resets after a stable run.
func (r *run) supervise(ctx context.Context) {
	backoff := engineRestartMin
	for {
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()

		cmd, err := r.spawn(ctx)
		if err != nil {
			r.logi.Errorf("引擎进程启动失败：%v", err)
		} else {
			started := time.Now()
			_ = cmd.Wait()
			r.mu.Lock()
			stopped := r.stopped
			r.mu.Unlock()
			if stopped {
				return
			}
			if time.Since(started) > engineStableFor {
				backoff = engineRestartMin
			}
			r.logi.Warnf("引擎进程退出，%v 后自动重启", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < engineRestartMax {
			backoff *= 2
		}
	}
}

// queryStatus reads the engine's localhost status endpoint.
func (r *run) queryStatus() (running bool, lastErr string, proxies []client.ProxyStatus) {
	cl := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := cl.Get("http://" + r.admin + "/status")
	if err != nil {
		return false, "引擎进程重启中", nil
	}
	defer resp.Body.Close()
	var out struct {
		Running bool                `json:"running"`
		Error   string              `json:"error"`
		Proxies []client.ProxyStatus `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "引擎状态解析失败", nil
	}
	return out.Running, out.Error, out.Proxies
}

// terminate asks the engine process to exit; the supervisor's Wait reaps it.
func (r *run) terminate() {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}
