package ssh

import (
	"context"
	"fmt"
	"net"

	gossh "golang.org/x/crypto/ssh"
)

func DialRemote(ctx context.Context, client *gossh.Client, remoteAddr string) (net.Conn, error) {
	conn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("dial remote %s through SSH: %w", remoteAddr, err)
	}
	setNoDelay(conn)
	return conn, nil
}
