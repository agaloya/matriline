package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Hooks ([hooks] in server.conf): programs of the admin run when something happens, to
// move, copy, upload or chain results without polling (user: "interactive like an API",
// no network port). They run one at a time, without a shell, at most 10 minutes each;
// a failure is logged and in state/events.log. Arguments and environment:
//
//	result <task> <output|weird|errors> <result folder>   MATRILINE_EVENT=result, MATRILINE_TASK,
//	                                                     MATRILINE_VERDICT, MATRILINE_RESULT_DIR
//	done   <summary>                                     MATRILINE_EVENT=done (queue drained)

type hookJob struct {
	prog string
	args []string
	env  []string
}

func (s *Server) hookLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case j := <-s.hooks:
			out, err := s.runChild(j.prog, j.args, j.env, s.conf().Root, 10*time.Minute)
			if err != nil {
				s.log.Errorf("hook %s %s: %v: %s", j.prog, strings.Join(j.args, " "), err, tail(string(out), 300))
				s.event("system", "hook %s %s failed: %v", filepath.Base(j.prog), j.args[0], err)
			}
		}
	}
}

// runChild runs an admin's program (hook, alert program): without a shell, in its own
// process group, at most limit and never after the server is told to stop (WaitDelay: a
// background program left behind cannot hold the server), without the SMTP password
// variable in its environment.
func (s *Server) runChild(prog string, args, env []string, dir string, limit time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, prog, args...)
	ownGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	secret := s.conf().MailPassEnv + "="
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, secret) {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (s *Server) queueHook(prog string, args, env []string) {
	select {
	case s.hooks <- hookJob{prog, args, env}:
	default:
		s.log.Warnf("hook queue full: %s %s not run", prog, strings.Join(args, " "))
	}
}

// hookResult reports a result that arrived in (or moved to) output/, weird/ or errors/.
func (s *Server) hookResult(taskID, rel string) {
	cfg := s.conf()
	if cfg.HookResult == "" || strings.HasPrefix(taskID, "~") {
		return
	}
	top, _ := splitSpool(rel)
	verdict := strings.TrimSuffix(top, "/")
	dir := filepath.Join(cfg.Root, filepath.FromSlash(rel))
	s.queueHook(cfg.HookResult, []string{"result", taskID, verdict, dir},
		[]string{"MATRILINE_EVENT=result", "MATRILINE_TASK=" + taskID, "MATRILINE_VERDICT=" + verdict, "MATRILINE_RESULT_DIR=" + dir})
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
