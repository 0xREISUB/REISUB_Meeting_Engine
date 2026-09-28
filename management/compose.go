package management

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const maxLogBytes = 48 * 1024

var managedServices = map[string]struct{}{
	"backend": {},
	"livekit": {},
}

type ServiceStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

type composeService struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

type DockerCompose struct {
	file       string
	projectDir string
	envFile    string
	secrets    func() []string
}

func NewDockerCompose(file, projectDir, envFile string, secrets func() []string) *DockerCompose {
	return &DockerCompose{file: file, projectDir: projectDir, envFile: envFile, secrets: secrets}
}

func (d *DockerCompose) Status(ctx context.Context) ([]ServiceStatus, error) {
	output, err := d.run(ctx, "ps", "--all", "--format", "json", "backend", "livekit")
	if err != nil {
		return nil, err
	}
	statuses := []ServiceStatus{{Name: "backend", State: "stopped"}, {Name: "livekit", State: "stopped"}}
	byName := map[string]int{"backend": 0, "livekit": 1}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxLogBytes)
	for scanner.Scan() {
		var row composeService
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("Compose durum yanıtı çözümlenemedi: %w", err)
		}
		if index, ok := byName[row.Service]; ok {
			statuses[index].State = strings.ToLower(row.State)
			statuses[index].Health = strings.ToLower(row.Health)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (d *DockerCompose) Action(ctx context.Context, service, action string) error {
	args, err := actionArguments(service, action)
	if err != nil {
		return err
	}
	_, err = d.run(ctx, args...)
	return err
}

func actionArguments(service, action string) ([]string, error) {
	if err := validateService(service); err != nil {
		return nil, err
	}
	switch action {
	case "start":
		return []string{"up", "--detach", "--no-build", service}, nil
	case "stop":
		return []string{"stop", service}, nil
	case "restart":
		return []string{"up", "--detach", "--no-build", "--force-recreate", service}, nil
	default:
		return nil, errors.New("İşlem desteklenmiyor")
	}
}

func (d *DockerCompose) Logs(ctx context.Context, service string) (string, error) {
	if err := validateService(service); err != nil {
		return "", err
	}
	output, err := d.run(ctx, "logs", "--tail=100", "--no-color", service)
	if err != nil {
		return "", err
	}
	if len(output) > maxLogBytes {
		output = output[len(output)-maxLogBytes:]
	}
	return d.redact(string(output)), nil
}

func (d *DockerCompose) Recreate(ctx context.Context, services ...string) error {
	if len(services) == 0 {
		return nil
	}
	args := []string{"up", "--detach", "--no-build", "--force-recreate"}
	for _, service := range services {
		if err := validateService(service); err != nil {
			return err
		}
		args = append(args, service)
	}
	_, err := d.run(ctx, args...)
	return err
}

func (d *DockerCompose) run(ctx context.Context, args ...string) ([]byte, error) {
	commandArgs := []string{
		"compose",
		"--project-directory", d.projectDir,
		"--env-file", d.envFile,
		"--file", d.file,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	output := &limitedBuffer{limit: maxLogBytes}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("Compose işlemi başarısız: %s", d.redact(strings.TrimSpace(output.String())))
	}
	return output.Bytes(), nil
}

func (d *DockerCompose) redact(value string) string {
	if d.secrets == nil {
		return value
	}
	for _, secret := range d.secrets() {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func validateService(service string) error {
	if _, ok := managedServices[service]; !ok {
		return errors.New("Servis bulunamadı")
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(value) >= buffer.limit {
		buffer.buffer.Reset()
		_, err := buffer.buffer.Write(value[len(value)-buffer.limit:])
		return written, err
	}
	if buffer.buffer.Len()+len(value) > buffer.limit {
		retained := buffer.limit - len(value)
		previous := append([]byte(nil), buffer.buffer.Bytes()[buffer.buffer.Len()-retained:]...)
		buffer.buffer.Reset()
		if _, err := buffer.buffer.Write(previous); err != nil {
			return 0, err
		}
	}
	_, err := buffer.buffer.Write(value)
	return written, err
}

func (buffer *limitedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}
