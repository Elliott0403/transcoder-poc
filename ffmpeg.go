package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// probeResult is what we need from ffprobe.
type probeResult struct {
	DurationSec float64
	HasAudio    bool
}

// probe runs ffprobe once before transcoding to get the total duration (the progress denominator)
// and whether the input has an audio track.
func probe(ctx context.Context, input string) (probeResult, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type",
		"-of", "json",
		input,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return probeResult{}, fmt.Errorf("ffprobe: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return probeResult{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	res := probeResult{}
	// Duration can be "N/A" for some inputs; keep 0 and skip intermediate progress in that case.
	if d, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		res.DurationSec = d
	}
	for _, s := range parsed.Streams {
		if s.CodecType == "audio" {
			res.HasAudio = true
		}
	}
	return res, nil
}

// runFFmpeg runs ffmpeg in dir and reports progress (0~100 of the encoded duration) parsed from
// `-progress pipe:1`, i.e. out_time_us / total duration.
func runFFmpeg(ctx context.Context, dir string, args []string, totalSec float64, onProgress func(pct float64)) error {
	full := append([]string{"-y", "-hide_banner", "-loglevel", "error", "-nostats", "-progress", "pipe:1"}, args...)
	cmd := exec.CommandContext(ctx, "ffmpeg", full...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok || totalSec <= 0 || onProgress == nil {
			continue
		}
		// out_time_ms is (despite its name) also microseconds; prefer out_time_us when present.
		if key != "out_time_us" && key != "out_time_ms" {
			continue
		}
		us, err := strconv.ParseFloat(value, 64) // "N/A" at the very beginning is skipped
		if err != nil {
			continue
		}
		onProgress(min(us/1e6/totalSec*100, 100))
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// clipArgs: a single MP4, width capped at 1280 (no upscaling), aligned with the current
// prod config (width 1280, quality 2).
func clipArgs(input, output string, preset string) []string {
	return []string{
		"-i", input,
		"-vf", "scale='min(1280,iw)':-2",
		"-c:v", "libx264", "-preset", preset, "-crf", "23", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		output,
	}
}

// vodHLSArgs: decode once, encode 3 HLS renditions (640/960/1280) plus the master playlist.
// Bitrates follow the current master playlist bandwidths (864/2296/5128 kbps incl. 128k audio).
func vodHLSArgs(input string, hasAudio bool, preset string) []string {
	args := []string{
		"-i", input,
		"-filter_complex",
		"[0:v]split=3[a][b][c];" +
			"[a]scale='min(640,iw)':-2[v640];" +
			"[b]scale='min(960,iw)':-2[v960];" +
			"[c]scale='min(1280,iw)':-2[v1280]",
	}

	streamMap := []string{}
	for i, label := range []string{"v640", "v960", "v1280"} {
		args = append(args, "-map", "["+label+"]")
		name := strings.TrimPrefix(label, "v")
		if hasAudio {
			args = append(args, "-map", "0:a:0")
			streamMap = append(streamMap, fmt.Sprintf("v:%d,a:%d,name:%s", i, i, name))
		} else {
			streamMap = append(streamMap, fmt.Sprintf("v:%d,name:%s", i, name))
		}
	}

	args = append(args,
		"-c:v", "libx264", "-preset", preset, "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-b:v:0", "750k", "-maxrate:v:0", "800k", "-bufsize:v:0", "1600k",
		"-b:v:1", "2100k", "-maxrate:v:1", "2200k", "-bufsize:v:1", "4400k",
		"-b:v:2", "4900k", "-maxrate:v:2", "5000k", "-bufsize:v:2", "10000k",
		// Aligned keyframes across renditions so ABR switching is seamless.
		"-force_key_frames", "expr:gte(t,n_forced*6)", "-sc_threshold", "0",
	)
	if hasAudio {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args,
		"-f", "hls", "-hls_time", "6", "-hls_playlist_type", "vod",
		"-hls_segment_filename", "file-%v-%05d.ts",
		"-var_stream_map", strings.Join(streamMap, " "),
		"-master_pl_name", "playlist.m3u8",
		"file-%v.m3u8",
	)
	return args
}

// vodRemuxArgs: repackage an HLS rendition into MP4 without re-encoding.
func vodRemuxArgs(playlist, output string) []string {
	return []string{
		"-i", playlist,
		"-c", "copy", "-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		output,
	}
}
