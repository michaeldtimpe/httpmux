package ssh

import (
	"fmt"
	"io"

	gossh "golang.org/x/crypto/ssh"
)

type PTYSession struct {
	Session *gossh.Session
	Stdin   io.WriteCloser
	Stdout  io.Reader
}

func OpenPTY(client *gossh.Client, cols, rows int, cmd string) (*PTYSession, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}

	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		session.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, fmt.Errorf("start command %q: %w", cmd, err)
	}

	return &PTYSession{
		Session: session,
		Stdin:   stdin,
		Stdout:  stdout,
	}, nil
}

func ResizePTY(session *gossh.Session, cols, rows int) error {
	return session.WindowChange(rows, cols)
}
