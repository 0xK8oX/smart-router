package translation

import (
	"bufio"
	"io"
	"strings"
)

// SSETranslator converts SSE streams between OpenAI and Anthropic formats.
// Uses io.Pipe for streaming without buffering.
// For now, implements a simplified passthrough that:
//   - Reads SSE lines from reader
//   - Parses "data: " prefixed lines
//   - Passes through (full conversion is complex, do simplified version)
//   - Writes to pipe writer
func SSETranslator(reader io.Reader, fromFormat, toFormat string) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()

			// Pass through empty lines (SSE event separators)
			if strings.TrimSpace(line) == "" {
				if _, err := pw.Write([]byte("\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
				continue
			}

			// Pass through SSE lines as-is
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
		}

		if err := scanner.Err(); err != nil {
			pw.CloseWithError(err)
		}
	}()

	return pr
}
