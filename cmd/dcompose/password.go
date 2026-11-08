package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func readPassword(input *os.File, output io.Writer, confirm bool) (string, error) {
	fd := int(input.Fd())
	interactive := term.IsTerminal(fd)
	var data []byte
	var err error
	if interactive {
		fmt.Fprint(output, "Password: ")
		data, err = term.ReadPassword(fd)
		fmt.Fprintln(output)
	} else {
		data, err = io.ReadAll(io.LimitReader(input, 4096))
	}
	if nil != err {
		return "", err
	}

	password := string(data)
	if !interactive {
		password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	}
	if "" == password {
		return "", errors.New("password must not be empty")
	}

	if interactive && confirm {
		fmt.Fprint(output, "Confirm password: ")
		repeated, err := term.ReadPassword(fd)
		fmt.Fprintln(output)
		if nil != err {
			return "", err
		}
		if password != string(repeated) {
			return "", errors.New("passwords do not match")
		}
	}
	return password, nil
}
