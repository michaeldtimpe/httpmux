package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/mtimpe/httpmux/internal/config"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type bastionConn struct {
	client   *gossh.Client
	channels int
	dead     bool
}

type BastionPool struct {
	mu          sync.Mutex
	cfg         config.BastionConfig
	sshCfg      config.SSHConfig
	conns       []*bastionConn
	hostKeyDB   gossh.HostKeyCallback
	maxSessions int
}

func NewBastionPool(bastionCfg config.BastionConfig, sshCfg config.SSHConfig) (*BastionPool, error) {
	knownHostsPath := bastionCfg.KnownHosts
	if knownHostsPath == "" {
		knownHostsPath = sshCfg.KnownHosts
	}

	hostKeyDB, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("loading known_hosts %q: %w", knownHostsPath, err)
	}

	pool := &BastionPool{
		cfg:         bastionCfg,
		sshCfg:      sshCfg,
		hostKeyDB:   hostKeyDB,
		maxSessions: bastionCfg.MaxSessions,
	}
	if pool.maxSessions <= 0 {
		pool.maxSessions = 8
	}
	return pool, nil
}

func (bp *BastionPool) getBastionConn(ctx context.Context) (*bastionConn, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	for _, bc := range bp.conns {
		if !bc.dead && bc.channels < bp.maxSessions {
			bc.channels++
			return bc, nil
		}
	}

	bp.mu.Unlock()
	client, err := bp.dialBastion(ctx)
	bp.mu.Lock()
	if err != nil {
		return nil, err
	}

	bc := &bastionConn{client: client, channels: 1}
	bp.conns = append(bp.conns, bc)

	go bp.keepalive(bc)
	return bc, nil
}

func (bp *BastionPool) releaseChannel(bc *bastionConn) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bc.channels > 0 {
		bc.channels--
	}
}

func (bp *BastionPool) dialBastion(ctx context.Context) (*gossh.Client, error) {
	authMethods, err := bp.bastionAuthMethods()
	if err != nil {
		return nil, fmt.Errorf("bastion auth: %w", err)
	}

	clientCfg := &gossh.ClientConfig{
		User:            bp.cfg.User,
		Auth:            authMethods,
		HostKeyCallback: bp.hostKeyDB,
		Timeout:         10 * time.Second,
	}

	addr := bp.cfg.Host
	slog.Info("connecting to bastion", "host", addr)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial bastion %s: %w", addr, err)
	}
	setNoDelay(conn)

	sshConn, chans, reqs, err := gossh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake with bastion %s: %w", addr, err)
	}
	return gossh.NewClient(sshConn, chans, reqs), nil
}

func (bp *BastionPool) DialTarget(ctx context.Context, target config.Target) (*gossh.Client, error) {
	bc, err := bp.getBastionConn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get bastion connection: %w", err)
	}

	tunnelConn, err := bc.client.Dial("tcp", target.Host)
	if err != nil {
		bp.releaseChannel(bc)
		bp.markDead(bc)
		return nil, fmt.Errorf("tunnel through bastion to %s: %w", target.Host, err)
	}
	setNoDelay(tunnelConn)

	authMethods, err := targetAuthMethods(target, bp.sshCfg)
	if err != nil {
		tunnelConn.Close()
		bp.releaseChannel(bc)
		return nil, fmt.Errorf("target auth for %s: %w", target.Name, err)
	}

	hostKeyCallback := bp.hostKeyDB
	if target.HostKeyFingerprint != "" {
		hostKeyCallback = fingerprintCallback(target.HostKeyFingerprint)
	}

	clientCfg := &gossh.ClientConfig{
		User:            target.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(tunnelConn, target.Host, clientCfg)
	if err != nil {
		tunnelConn.Close()
		bp.releaseChannel(bc)
		return nil, fmt.Errorf("ssh handshake with target %s: %w", target.Name, err)
	}

	targetClient := gossh.NewClient(sshConn, chans, reqs)

	go func() {
		targetClient.Wait()
		bp.releaseChannel(bc)
	}()

	return targetClient, nil
}

func (bp *BastionPool) keepalive(bc *bastionConn) {
	interval := time.Duration(bp.cfg.Keepalive) * time.Second
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		_, _, err := bc.client.SendRequest("keepalive@openssh.com", true, nil)
		if err != nil {
			slog.Warn("bastion keepalive failed, marking connection dead", "error", err)
			bp.markDead(bc)
			return
		}
	}
}

func (bp *BastionPool) markDead(bc *bastionConn) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bc.dead = true
}

func (bp *BastionPool) Close() error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	for _, bc := range bp.conns {
		bc.client.Close()
	}
	bp.conns = nil
	return nil
}

func (bp *BastionPool) bastionAuthMethods() ([]gossh.AuthMethod, error) {
	if bp.sshCfg.UseAgent {
		agent, err := sshAgent()
		if err == nil {
			return []gossh.AuthMethod{agent}, nil
		}
		slog.Warn("SSH agent unavailable, falling back to key file", "error", err)
	}
	key, err := loadPrivateKey(bp.cfg.PrivateKey, bp.cfg.Passphrase)
	if err != nil {
		return nil, err
	}
	return []gossh.AuthMethod{gossh.PublicKeys(key)}, nil
}

func targetAuthMethods(target config.Target, sshCfg config.SSHConfig) ([]gossh.AuthMethod, error) {
	if sshCfg.UseAgent {
		agent, err := sshAgent()
		if err == nil {
			return []gossh.AuthMethod{agent}, nil
		}
		slog.Warn("SSH agent unavailable for target, falling back to key file", "target", target.Name, "error", err)
	}
	key, err := loadPrivateKey(target.PrivateKey, target.Passphrase)
	if err != nil {
		return nil, err
	}
	return []gossh.AuthMethod{gossh.PublicKeys(key)}, nil
}

func loadPrivateKey(path, passphrase string) (gossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading private key %q: %w", path, err)
	}
	if passphrase != "" {
		return gossh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	}
	return gossh.ParsePrivateKey(data)
}

func fingerprintCallback(expected string) gossh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		actual := gossh.FingerprintSHA256(key)
		if actual != expected {
			return fmt.Errorf("host key fingerprint mismatch for %s: expected %s, got %s", hostname, expected, actual)
		}
		return nil
	}
}

func setNoDelay(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
}
