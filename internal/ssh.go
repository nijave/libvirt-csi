package internal

import (
	"bytes"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SshRunner struct {
	Host       string
	User       string
	KnownHosts string
	PrivateKey string
}

func (r *SshRunner) RunCommand(cmd string) (string, string, error) {
	conn, err := r.makeClient()
	if err != nil {
		return "", "", err
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &stdout

	var stderr bytes.Buffer
	session.Stderr = &stderr

	err = session.Run(cmd)

	return stdout.String(), stderr.String(), err
}

func (r *SshRunner) makeClient() (*ssh.Client, error) {
	verifier, err := knownhosts.New(r.KnownHosts)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey([]byte(r.PrivateKey))
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		HostKeyCallback: verifier,
		User:            r.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
	}

	return ssh.Dial("tcp", r.Host, config)
}
