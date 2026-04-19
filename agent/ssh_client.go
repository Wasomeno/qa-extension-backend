package agent

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient wraps an SSH client connection
type SSHClient struct {
	client *ssh.Client
	host   string
}

// NewSSHClient creates a new SSH client from environment variables
func NewSSHClient() (*SSHClient, error) {
	host := os.Getenv("FIX_SSH_HOST")
	if host == "" {
		return nil, fmt.Errorf("FIX_SSH_HOST not set")
	}

	user := os.Getenv("FIX_SSH_USER")
	if user == "" {
		user = "root"
	}

	port := os.Getenv("FIX_SSH_PORT")
	if port == "" {
		port = "22"
	}

	// Get authentication method
	authMethod, err := getSSHAuthMethod()
	if err != nil {
		return nil, fmt.Errorf("failed to get SSH auth: %w", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // For simplicity; use ssh.FixedHostKey in production
		Timeout: 30 * time.Second,
	}

	// Connect to SSH server
	addr := fmt.Sprintf("%s:%s", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial SSH server %s: %w", addr, err)
	}

	return &SSHClient{
		client: client,
		host:   host,
	}, nil
}

// getSSHAuthMethod returns the SSH authentication method
func getSSHAuthMethod() (ssh.AuthMethod, error) {
	// Try private key first
	keyPath := os.Getenv("FIX_SSH_KEY_PATH")
	if keyPath == "" {
		// Default to ~/.ssh/id_rsa
		home, err := os.UserHomeDir()
		if err == nil {
			keyPath = home + "/.ssh/id_rsa"
		}
	}

	if _, err := os.Stat(keyPath); err == nil {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH key: %w", err)
		}

		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH key: %w", err)
		}

		return ssh.PublicKeys(signer), nil
	}

	// Try password
	password := os.Getenv("FIX_SSH_PASSWORD")
	if password != "" {
		return ssh.Password(password), nil
	}

	// Try SSH agent
	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		return nil, fmt.Errorf("no SSH authentication method available (set FIX_SSH_KEY_PATH or FIX_SSH_PASSWORD)")
	}), nil
}

// NewSession creates a new SSH session
func (c *SSHClient) NewSession() (*ssh.Session, error) {
	return c.client.NewSession()
}

// Close closes the SSH connection
func (c *SSHClient) Close() error {
	return c.client.Close()
}

// Host returns the SSH host
func (c *SSHClient) Host() string {
	return c.host
}

// RunCommand runs a command on the remote server and returns the output
func (c *SSHClient) RunCommand(cmd string) ([]byte, error) {
	session, err := c.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	return session.CombinedOutput(cmd)
}

// RunCommandInDir runs a command in a specific directory on the remote server
func (c *SSHClient) RunCommandInDir(cmd string, dir string) ([]byte, error) {
	fullCmd := fmt.Sprintf("cd %s && %s", dir, cmd)
	return c.RunCommand(fullCmd)
}

// IsSSHEnabled returns true if SSH is configured
func IsSSHEnabled() bool {
	return os.Getenv("FIX_SSH_HOST") != ""
}

// GetSSHHost returns the SSH host if configured
func GetSSHHost() string {
	return os.Getenv("FIX_SSH_HOST")
}

// Ensure SSHClient implements the interface for remote execution
var _ RemoteExecutor = (*SSHClient)(nil)

// RemoteExecutor defines the interface for remote command execution
type RemoteExecutor interface {
	RunCommand(cmd string) ([]byte, error)
	RunCommandInDir(cmd string, dir string) ([]byte, error)
	Close() error
}
