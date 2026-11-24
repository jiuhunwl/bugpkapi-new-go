package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
)

func TestOpenRedisEnablesContextTimeouts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		serverDone <- serveRedisHandshake(connection)
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := openRedis(ctx, config.RedisConfig{
		Host: host,
		Port: port,
	})
	if err != nil {
		t.Fatalf("openRedis() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if !client.Options().ContextTimeoutEnabled {
		t.Fatal("Redis commands can outlive their short request context and consume the upstream polling budget")
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("fake Redis server error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func serveRedisHandshake(connection net.Conn) error {
	reader := bufio.NewReader(connection)
	for {
		command, err := readRedisCommand(reader)
		if err != nil {
			return err
		}
		switch strings.ToLower(command) {
		case "hello":
			if _, err := connection.Write([]byte("-ERR unknown command 'hello'\r\n")); err != nil {
				return err
			}
		case "ping":
			_, err := connection.Write([]byte("+PONG\r\n"))
			return err
		default:
			if _, err := connection.Write([]byte("+OK\r\n")); err != nil {
				return err
			}
		}
	}
}

func readRedisCommand(reader *bufio.Reader) (string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(header) < 4 || header[0] != '*' {
		return "", fmt.Errorf("unexpected Redis request header %q", header)
	}
	count, err := strconv.Atoi(strings.TrimSpace(header[1:]))
	if err != nil || count <= 0 {
		return "", fmt.Errorf("invalid Redis request header %q", header)
	}
	var command string
	for index := 0; index < count; index++ {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if len(lengthLine) < 4 || lengthLine[0] != '$' {
			return "", fmt.Errorf("unexpected Redis bulk header %q", lengthLine)
		}
		length, err := strconv.Atoi(strings.TrimSpace(lengthLine[1:]))
		if err != nil || length < 0 {
			return "", fmt.Errorf("invalid Redis bulk header %q", lengthLine)
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		if index == 0 {
			command = string(value[:length])
		}
	}
	return command, nil
}
