package desktop

import opus "gopkg.in/hraban/opus.v2"

func newTestEncoder() (*opus.Encoder, error) {
	return opus.NewEncoder(sampleRate, 1, opus.AppVoIP)
}
