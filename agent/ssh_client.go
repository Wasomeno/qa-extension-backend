package agent

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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
	// Try SSH agent first (same as command-line ssh)
	if auth, err := sshAgentAuth(); err == nil {
		return auth, nil
	}

	// Try private key from env or default paths
	keyPath := os.Getenv("FIX_SSH_KEY_PATH")
	if keyPath == "" {
		// Try default key paths (same as ssh command)
		home, err := os.UserHomeDir()
		if err == nil {
			// Try common key files in order
			defaultKeys := []string{
				home + "/.ssh/id_ed25519",
				home + "/.ssh/id_rsa",
				home + "/.ssh/id_ecdsa",
				home + "/.ssh/id_dsa",
			}
			for _, path := range defaultKeys {
				if _, err := os.Stat(path); err == nil {
					keyPath = path
					break
				}
			}
		}
	}

	if keyPath != "" {
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

	// Try password from env
	if password := os.Getenv("FIX_SSH_PASSWORD"); password != "" {
		return ssh.Password(password), nil
	}

	return nil, fmt.Errorf("no SSH authentication method available (tried SSH agent, default keys, and password)")
}

// sshAgentAuth tries to use the running SSH agent
func sshAgentAuth() (ssh.AuthMethod, error) {
	// Get SSH agent socket from environment
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
	}

	// Connect to SSH agent
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH agent: %w", err)
	}

	agentClient := agent.NewClient(conn)
	return ssh.PublicKeysCallback(agentClient.Signers), nil
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
