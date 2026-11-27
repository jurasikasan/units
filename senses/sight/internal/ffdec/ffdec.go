// Package ffdec decodes H.264 through ffmpeg: an ffmpeg process per stream,
// fed Annex B access units on its stdin, giving pictures back on its stdout
// as YUV4MPEG2 (full-range 4:2:0), one picture per access unit, in
// presentation order.
package ffdec

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Args are ffmpeg's: raw H.264 in with as little probing as it allows, every
// decoded picture out as it is (no frame rate conversion that would drop or
// repeat one), full range so that a picture is a JPEG's YCbCr as it stands.
// Not -fflags nobuffer: with it ffmpeg drops pictures of a piped stream, and
// one picture per access unit is what places pictures in time.
var Args = []string{
	"-hide_banner", "-loglevel", "error",
	"-flags", "low_delay",
	"-probesize", "32", "-analyzeduration", "0",
	"-f", "h264", "-i", "pipe:0",
	"-an", "-fps_mode", "passthrough",
	"-pix_fmt", "yuvj420p", "-strict", "-1",
	"-flush_packets", "1",
	"-f", "yuv4mpegpipe", "pipe:1",
}

// Decoder is one running ffmpeg.
type Decoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    chan *image.YCbCr
	stderr *tail

	mu  sync.Mutex
	err error // why the pictures stopped, when not the end of the input
}

// Start runs bin (the ffmpeg binary, a name on PATH or a path).
func Start(bin string) (*Decoder, error) {
	cmd := exec.Command(bin, Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	d := &Decoder{cmd: cmd, stdin: stdin, out: make(chan *image.YCbCr, 4), stderr: &tail{}}
	cmd.Stderr = d.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	go d.read(stdout)
	return d, nil
}

// Write feeds one access unit.
func (d *Decoder) Write(au []byte) error {
	if _, err := d.stdin.Write(au); err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, d.stderr)
	}
	return nil
}

// CloseInput ends the stream: ffmpeg decodes what it holds, then exits, and
// Pictures closes after the last picture.
func (d *Decoder) CloseInput() error { return d.stdin.Close() }

// Pictures are the decoded pictures, in presentation order.
func (d *Decoder) Pictures() <-chan *image.YCbCr { return d.out }

// Err is why the pictures stopped early, once Pictures is closed; nil when
// they stopped because the input ended.
func (d *Decoder) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *Decoder) read(stdout io.Reader) {
	defer close(d.out)
	err := ReadY4M(stdout, func(p *image.YCbCr) { d.out <- p })
	if werr := d.cmd.Wait(); err == nil && werr != nil {
		err = fmt.Errorf("ffmpeg: %w (%s)", werr, d.stderr)
	}
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}

// ReadY4M reads a YUV4MPEG2 stream of 4:2:0 pictures and calls each with a
// picture of its own. An empty stream is no error.
func ReadY4M(r io.Reader, each func(*image.YCbCr)) error {
	br := bufio.NewReaderSize(r, 1<<20)
	header, err := br.ReadString('\n')
	if err == io.EOF && header == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("y4m: header: %w", err)
	}
	w, h, err := y4mHeader(header)
	if err != nil {
		return err
	}
	cw, ch := (w+1)/2, (h+1)/2
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF && line == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("y4m: frame header: %w", err)
		}
		if !strings.HasPrefix(line, "FRAME") {
			return fmt.Errorf("y4m: %q where a FRAME was due", strings.TrimSpace(line))
		}
		buf := make([]byte, w*h+2*cw*ch)
		if _, err := io.ReadFull(br, buf); err != nil {
			return fmt.Errorf("y4m: frame: %w", err)
		}
		each(&image.YCbCr{
			Y:              buf[:w*h],
			Cb:             buf[w*h : w*h+cw*ch],
			Cr:             buf[w*h+cw*ch:],
			YStride:        w,
			CStride:        cw,
			SubsampleRatio: image.YCbCrSubsampleRatio420,
			Rect:           image.Rect(0, 0, w, h),
		})
	}
}

func y4mHeader(line string) (w, h int, err error) {
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "YUV4MPEG2" {
		return 0, 0, fmt.Errorf("y4m: not a YUV4MPEG2 stream")
	}
	for _, p := range f[1:] {
		switch p[0] {
		case 'W':
			w, err = strconv.Atoi(p[1:])
		case 'H':
			h, err = strconv.Atoi(p[1:])
		case 'C':
			if !strings.HasPrefix(p, "C420") {
				err = fmt.Errorf("y4m: colour space %s, not 4:2:0", p[1:])
			}
		}
		if err != nil {
			return 0, 0, err
		}
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("y4m: no size in %q", strings.TrimSpace(line))
	}
	return w, h, nil
}

// tail keeps the last of ffmpeg's stderr, for an error to say why.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.b))
}
