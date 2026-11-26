// Package prompt asks the person at the terminal.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// stdin is read through one buffer, so an answer typed ahead is not lost between questions.
var stdin = bufio.NewReader(os.Stdin)

// Confirm asks a yes-or-no question whose answer is yes unless told otherwise. Without
// someone to answer it, it fails with noTerminal: --yes is how a script says yes.
func Confirm(question, noTerminal, declined string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New(noTerminal)
	}
	fmt.Print(question + " (Y/n) ")
	answer, err := stdin.ReadString('\n')
	if err != nil && answer == "" {
		fmt.Println()
		return errors.New("no answer, so nothing was changed")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return nil
	}
	return errors.New(declined)
}
