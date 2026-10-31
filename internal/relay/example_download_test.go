package relay

import (
	"strings"
	"testing"
)

func TestDocumentedExampleDownloads(t *testing.T) {
	script := documentedBash(t, "raw.githubusercontent.com")
	for _, tc := range []struct {
		name, failed string
		calls        int
	}{
		{"all downloads", "", 3},
		{"first download fails", personalExample, 1},
		{"second download fails", workExample, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, root, environment := snippetFixture(t, personalProfile)
			prelude := `curl() {
 output=""
 url=""
 while [ "$#" -gt 0 ]; do
   case "$1" in
     --output) shift; output=$1 ;;
     https://*) url=$1 ;;
   esac
   shift
 done
 printf '%s\n' "$url" >> "$DOWNLOAD_LOG"
 printf 'synthetic download' > "$output"
 case "$url" in
   */"$FAIL_FILE") return 22 ;;
 esac
}
`
			environment = append(environment, "DOWNLOAD_LOG="+dir+"/download-calls", "FAIL_FILE="+tc.failed)
			output, err := runSnippet(t, prelude+script, environment)
			if (err == nil) != (tc.failed == "") {
				t.Fatalf("failure=%q error=%v output=%s", tc.failed, err, output)
			}
			calls, readErr := root.ReadFile("download-calls")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if count := len(strings.Fields(string(calls))); count != tc.calls {
				t.Fatalf("download calls=%d want=%d", count, tc.calls)
			}
		})
	}
}
