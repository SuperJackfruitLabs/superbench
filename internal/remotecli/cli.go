// Package remotecli is `bench workspace remote-credential` and
// `bench workspace remote-status`: thin clients of benchd's /v2/remote/*
// socket routes. The lease token is read from --token or BENCH_LEASE_TOKEN,
// sent only in the request body and never printed.
package remotecli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
)

const maxPublicKeyFile = 8 << 10

func Run(ctx context.Context, op string, args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("workspace "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	socket := f.String("socket", "", "benchd Unix socket (required)")
	switch op {
	case "remote-credential":
		public := f.String("public-key", "", "file holding one ssh-ed25519 or ecdsa-sha2-nistp256 public key (required)")
		out := f.String("certificate-out", "", "write the certificate here (0644), as OpenSSH's <key>-cert.pub")
		token := f.String("token", "", "lease token; defaults to BENCH_LEASE_TOKEN")
		if err := f.Parse(args); err != nil {
			return err
		}
		if *socket == "" || *public == "" || f.NArg() != 1 {
			return errors.New("usage: bench workspace remote-credential --socket PATH --public-key FILE [--certificate-out FILE] [--token TOKEN] key")
		}
		if *token == "" {
			*token = os.Getenv("BENCH_LEASE_TOKEN")
		}
		if *token == "" {
			return errors.New("lease token required: set BENCH_LEASE_TOKEN or pass --token")
		}
		key, err := readPublicKey(*public)
		if err != nil {
			return err
		}
		c, err := client.WriterCertificate(ctx, *socket, remoteproto.CredentialRequest{Key: f.Arg(0), LeaseToken: *token, PublicKey: key})
		if errors.Is(err, remoteproto.ErrInvalid) {
			return errors.New("invalid remote credential request: check the key, the lease token and the public key")
		}
		if err != nil {
			return err
		}
		if *out != "" {
			if err := writeCertificate(*out, c.Certificate); err != nil {
				return err
			}
		}
		return emit(stdout, c)
	case "remote-status":
		if err := f.Parse(args); err != nil {
			return err
		}
		if *socket == "" || f.NArg() > 1 {
			return errors.New("usage: bench workspace remote-status --socket PATH [key]")
		}
		s, err := client.RemoteStatus(ctx, *socket, remoteproto.StatusRequest{Key: f.Arg(0)})
		if err != nil {
			return err
		}
		return emit(stdout, s)
	}
	return fmt.Errorf("unknown remote operation %q", op)
}

func readPublicKey(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxPublicKeyFile+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxPublicKeyFile {
		return "", errors.New("public key file over 8 KiB")
	}
	return strings.TrimSpace(string(raw)), nil
}

func writeCertificate(path, certificate string) error {
	if err := os.WriteFile(path, []byte(certificate+"\n"), 0644); err != nil {
		return err
	}
	return os.Chmod(path, 0644)
}

func emit(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}
