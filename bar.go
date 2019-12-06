package main

import (
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"barista.run"
	"barista.run/bar"
	"barista.run/colors"
	"barista.run/format"
	"barista.run/modules/battery"
	"barista.run/modules/clock"
	"barista.run/modules/diskspace"
	"barista.run/modules/media"
	"barista.run/modules/meminfo"
	"barista.run/modules/shell"
	"barista.run/modules/sysinfo"
	"barista.run/outputs"
	"barista.run/pango"

	colorful "github.com/lucasb-eyer/go-colorful"
)

const (
	iconPrev                 = "\uf04a"
	iconPlay                 = "\uf04b"
	iconPause                = "\uf04c"
	iconNext                 = "\uf04e"
	iconBrightness           = "\uf0eb"
	iconSoundLow             = "\uf027"
	iconSoundHigh            = "\uf028"
	iconSoundMuted           = "\uf026"
	iconMemory               = "\uf1c0"
	iconHDD                  = "\uf0a0"
	iconCPU                  = "\uf0e4"
	iconBatteryCharging      = "\uf0e7"
	iconBatteryEmpty         = "\uf244"
	iconBatteryOneQuarter    = "\uf243"
	iconBatteryHalf          = "\uf242"
	iconBatteryThreeQuarters = "\uf241"
	iconBatteryFull          = "\uf240"
	iconCalendar             = "\uf133"
	iconTime                 = "\uf017"

	iconFont = "FontAwesome"
)

var batteryStatus = battery.All().Output(func(i battery.Info) bar.Output {
	if i.Status == battery.Disconnected || i.Status == battery.Unknown {
		return nil
	}

	var ico = iconBatteryFull
	remaining := i.RemainingPct()
	if remaining < 5 {
		ico = iconBatteryEmpty
	} else if remaining <= 25 {
		ico = iconBatteryOneQuarter
	} else if remaining <= 50 {
		ico = iconBatteryHalf
	} else if remaining <= 80 {
		ico = iconBatteryThreeQuarters
	}

	chargingIcon := icon(iconBatteryCharging).Color(colors.Scheme("degraded"))
	icon := icon(ico)
	switch {
	case remaining <= 15:
		icon.Color(colors.Scheme("bad"))
	case remaining <= 25:
		icon.Color(colors.Scheme("degraded"))
	case remaining >= 90:
		icon.Color(colors.Scheme("good"))
	}

	text := text("%d%%", i.RemainingPct())

	if i.Status == battery.Charging {
		return outputs.Group(
			chargingIcon,
			icon,
			text,
		)
	}

	return outputs.Group(icon, text)
})

var localTime = clock.Local().
	Output(time.Minute, func(now time.Time) bar.Output {
		return outputs.Group(
			icon(iconCalendar),
			text(now.Format("Mon Jan 2 ")),
			icon(iconTime),
			text(now.Format("15:04")),
			text(" "),
		)
	})

var volume = DefaultSink().Output(func(v Volume) bar.Output {
	if v.Mute {
		return outputs.Group(
			icon(iconSoundMuted).Color(colors.Scheme("bad")),
			text("muted"),
		)
	}
	var ico = iconSoundLow
	pct := v.Pct()
	if pct > 66 {
		ico = iconSoundHigh
	}
	return outputs.Group(
		icon(ico),
		text("%2d%%", pct),
	)
})

var cpuLoad = sysinfo.New().Output(func(s sysinfo.Info) bar.Output {
	icon := icon(iconCPU)
	load := int(s.Loads[1] * 100)
	text := text("%d%%", load)
	out := outputs.Group(icon, text)
	// Load averages are unusually high for a few minutes after boot.
	if s.Uptime < 10*time.Minute {
		// so don't add colours until 10 minutes after system start.
		return out
	}

	switch {
	case load >= 150:
		icon.Color(colors.Scheme("bad"))
		text.Color(colors.Scheme("bad"))
	case load >= 100:
		icon.Color(colors.Scheme("degraded"))
		text.Color(colors.Scheme("degraded"))
	}

	return out
})

var freeMemory = meminfo.New().Output(func(m meminfo.Info) bar.Output {
	icon := icon(iconMemory)
	text := text(format.IBytesize(m.Available()))
	out := outputs.Group(icon, text)

	freeGigs := m.Available().Gigabytes()
	switch {
	case freeGigs < 0.5:
		out.Urgent(true)
	case freeGigs < 1:
		icon.Color(colors.Scheme("bad"))
		text.Color(colors.Scheme("bad"))
	case freeGigs < 2:
		icon.Color(colors.Scheme("degraded"))
		text.Color(colors.Scheme("degraded"))
	case freeGigs > 12:
		icon.Color(colors.Scheme("good"))
		text.Color(colors.Scheme("good"))
	}

	return out
})

var brightness = shell.New("light").Output(func(output string) bar.Output {
	n, _ := strconv.ParseFloat(output, 64)
	return outputs.Group(
		icon(iconBrightness),
		text("%d%%", int(n)),
	)
})

var hddSpace = diskspace.New("/home").Output(func(i diskspace.Info) bar.Output {
	return outputs.Group(
		icon(iconHDD),
		text(format.IBytesize(i.Available)),
	)
})

var spotifyControls = media.New("spotify").Output(func(m media.Info) bar.Output {
	if m.PlaybackStatus == media.Stopped || m.PlaybackStatus == media.Disconnected {
		return nil
	}

	out := new(outputs.SegmentGroup)
	out.Append(outputs.Pango(icon(iconPrev)).OnClick(ifLeft(m.Previous)))
	if m.Playing() {
		out.Append(outputs.Pango(icon(iconPause)).OnClick(ifLeft(m.Pause)))
	} else {
		out.Append(outputs.Pango(icon(iconPlay)).OnClick(ifLeft(m.Play)))
	}
	out.Append(outputs.Pango(icon(iconNext)).OnClick(ifLeft(m.Next)))

	artist := truncate(m.Artist, 20)
	title := truncate(m.Title, 40-len(artist))
	if len(title) < 20 {
		artist = truncate(m.Artist, 40-len(title))
	}

	out.Append(text("%s - %s", title, artist))
	return out
})

func main() {
	colors.LoadBarConfig()
	bg := colors.Scheme("background")
	fg := colors.Scheme("statusline")
	if fg != nil && bg != nil {
		iconColor := fg.Colorful().BlendHcl(bg.Colorful(), 0.2).Clamped()
		colors.Set("dim-icon", iconColor)
		_, _, v := fg.Colorful().Hsv()
		if v < 0.3 {
			v = 0.3
		}
		colors.Set("bad", colorful.Hcl(40, 1.0, v).Clamped())
		colors.Set("degraded", colorful.Hcl(90, 1.0, v).Clamped())
		colors.Set("good", colorful.Hcl(120, 1.0, v).Clamped())
	}

	panic(barista.Run(
		spotifyControls,
		brightness,
		volume,
		freeMemory,
		hddSpace,
		cpuLoad,
		batteryStatus,
		localTime,
	))
}

func truncate(in string, l int) string {
	if len([]rune(in)) <= l {
		return in
	}
	return string([]rune(in)[:l-1]) + "⋯"
}

func hms(d time.Duration) (h int, m int, s int) {
	h = int(d.Hours())
	m = int(d.Minutes()) % 60
	s = int(d.Seconds()) % 60
	return
}

func formatMediaTime(d time.Duration) string {
	h, m, s := hms(d)
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func home(path string) string {
	usr, err := user.Current()
	if err != nil {
		panic(err)
	}
	return filepath.Join(usr.HomeDir, path)
}

func text(txt string, args ...interface{}) *pango.Node {
	return pango.Text(fmt.Sprintf(txt, args...)).XXSmall()
}

func icon(icon string) *pango.Node {
	return pango.Text(icon).Font(iconFont).Color(colors.Scheme("dim-icon")).XSmall()
}

func ifLeft(dofn func()) func(bar.Event) {
	return func(e bar.Event) {
		if e.Button == bar.ButtonLeft {
			dofn()
		}
	}
}
