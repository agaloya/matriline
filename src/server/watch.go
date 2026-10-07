package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/i18n"
)

// cmdWatch is the terminal live view: the 'live' command every 2 s on a cleared screen.
// Line input (no raw terminal mode in the standard library): a job number, i or q, then
// Enter.
func cmdWatch(cfgPath string) error {
	enableVT()
	keys := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			keys <- strings.TrimSpace(sc.Text())
		}
		close(keys)
	}()
	sel, full := 1, false
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		args := []string{"live", strconv.Itoa(sel)}
		if full {
			args = append(args, "full")
		}
		out, err := call(cfgPath, args)
		if err == nil {
			out = withPicture(cfgPath, out, sel)
		}
		fmt.Print("\033[H\033[2J")
		if err != nil {
			fmt.Println("error:", err)
		} else {
			fmt.Print(out)
		}
		fmt.Print("\n" + i18n.T("[number] Enter: that job   i Enter: whole input on/off   q Enter: quit (or Ctrl+C)") + "\n")
		select {
		case k, ok := <-keys:
			if !ok || k == "q" {
				return nil
			}
			if k == "i" {
				full = !full
			} else if n, err := strconv.Atoi(k); err == nil && n > 0 {
				sel = n
			}
		case <-t.C:
		}
	}
}
