package main

import "os"

// the tests' many inits must not reach the person's list of projects (common/projects)
func init() { os.Setenv("MATRILINE_NO_REGISTRY", "1") }
