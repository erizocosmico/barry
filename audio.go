// Moved from https://github.com/soumya92/barista/tree/master/modules/volume
// because the volume package does not work unless ALSA C headers are available.
package main

import (
	"C"
	"fmt"
	"os"
	"time"

	"barista.run/bar"
	"barista.run/base/value"
	l "barista.run/logging"
	"barista.run/outputs"
	"github.com/godbus/dbus/v5"
	"golang.org/x/time/rate"
)

// PulseAudio implementation.
type paModule struct {
	sinkName string
}

type paController struct {
	sink dbus.BusObject
}

func dialAndAuth(addr string) (*dbus.Conn, error) {
	conn, err := dbus.Dial(addr)
	if err != nil {
		return nil, err
	}
	err = conn.Auth(nil)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func openPulseAudio() (*dbus.Conn, error) {
	// Pulse defaults to creating its socket in a well-known place under
	// XDG_RUNTIME_DIR. For Pulse instances created by systemd, this is the
	// only reliable way to contact Pulse via D-Bus, since Pulse is created
	// on a per-user basis, but the session bus is created once for every
	// session, and a user can have multiple sessions.
	xdgDir := os.Getenv("XDG_RUNTIME_DIR")
	if xdgDir != "" {
		addr := fmt.Sprintf("unix:path=%s/pulse/dbus-socket", xdgDir)
		return dialAndAuth(addr)
	}

	// Couldn't find the PulseAudio bus on the fast path, so look for it
	// by querying the session bus.
	bus, err := dbus.SessionBusPrivate()
	if err != nil {
		return nil, err
	}
	defer bus.Close()
	err = bus.Auth(nil)
	if err != nil {
		return nil, err
	}

	locator := bus.Object("org.PulseAudio1", "/org/pulseaudio/server_lookup1")
	path, err := locator.GetProperty("org.PulseAudio.ServerLookup1.Address")
	if err != nil {
		return nil, err
	}

	return dialAndAuth(path.Value().(string))
}

// Sink creates a PulseAudio volume module for a named sink.
func Sink(sinkName string) *Module {
	m := createModule(&paModule{sinkName: sinkName})
	if sinkName == "" {
		sinkName = "default"
	}
	l.Labelf(m, "pulse:%s", sinkName)
	return m
}

// DefaultSink creates a PulseAudio volume module that follows the default sink.
func DefaultSink() *Module {
	return Sink("")
}

func (c *paController) setVolume(newVol int64) error {
	return c.sink.Call(
		"org.freedesktop.DBus.Properties.Set",
		0,
		"org.PulseAudio.Core1.Device",
		"Volume",
		dbus.MakeVariant([]uint32{uint32(newVol)}),
	).Err
}

func (c *paController) setMuted(muted bool) error {
	return c.sink.Call(
		"org.freedesktop.DBus.Properties.Set",
		0,
		"org.PulseAudio.Core1.Device",
		"Mute",
		dbus.MakeVariant(muted),
	).Err
}

func listen(core dbus.BusObject, signal string, objects ...dbus.ObjectPath) error {
	return core.Call(
		"org.PulseAudio.Core1.ListenForSignal",
		0,
		"org.PulseAudio.Core1."+signal,
		objects,
	).Err
}

func openSink(conn *dbus.Conn, core dbus.BusObject, sinkPath dbus.ObjectPath) (dbus.BusObject, error) {
	sink := conn.Object("org.PulseAudio.Core1.Sink", sinkPath)
	if err := listen(core, "Device.VolumeUpdated", sinkPath); err != nil {
		return nil, err
	}
	return sink, listen(core, "Device.MuteUpdated", sinkPath)
}

func openSinkByName(conn *dbus.Conn, core dbus.BusObject, name string) (dbus.BusObject, error) {
	var path dbus.ObjectPath
	err := core.Call("org.PulseAudio.Core1.GetSinkByName", 0, name).Store(&path)
	if err != nil {
		return nil, err
	}
	return openSink(conn, core, path)
}

func openFallbackSink(conn *dbus.Conn, core dbus.BusObject) (dbus.BusObject, error) {
	path, err := core.GetProperty("org.PulseAudio.Core1.FallbackSink")
	if err != nil {
		return nil, err
	}
	return openSink(conn, core, path.Value().(dbus.ObjectPath))
}

func getVolume(sink dbus.BusObject) (Volume, error) {
	v := Volume{}
	v.Min = 0

	max, err := sink.GetProperty("org.PulseAudio.Core1.Device.BaseVolume")
	if err != nil {
		return v, err
	}
	v.Max = int64(max.Value().(uint32))

	vol, err := sink.GetProperty("org.PulseAudio.Core1.Device.Volume")
	if err != nil {
		return v, err
	}

	// Take the volume as the average across all channels.
	var totalVol int64
	channels := vol.Value().([]uint32)
	for _, ch := range channels {
		totalVol += int64(ch)
	}
	v.Vol = totalVol / int64(len(channels))

	mute, err := sink.GetProperty("org.PulseAudio.Core1.Device.Mute")
	if err != nil {
		return v, err
	}
	v.Mute = mute.Value().(bool)
	v.controller = &paController{sink}
	return v, nil
}

func (m *paModule) worker(s *value.ErrorValue) {
	conn, err := openPulseAudio()
	if s.Error(err) {
		return
	}
	defer conn.Close()

	core := conn.Object("org.PulseAudio.Core1", "/org/pulseaudio/core1")

	var sink dbus.BusObject
	if m.sinkName != "" {
		sink, err = openSinkByName(conn, core, m.sinkName)
	} else {
		sink, err = openFallbackSink(conn, core)
		if err == nil {
			err = listen(core, "FallbackSinkUpdated")
		}
	}
	if s.Error(err) {
		return
	}
	if s.SetOrError(getVolume(sink)) {
		return
	}

	signals := make(chan *dbus.Signal, 10)
	conn.Signal(signals)

	// Listen for signals from D-Bus, and update appropriately.
	for signal := range signals {
		// If the fallback sink changed, open the new one.
		if m.sinkName == "" && signal.Path == core.Path() {
			sink, err = openFallbackSink(conn, core)
			if s.Error(err) {
				return
			}
		}
		if s.SetOrError(getVolume(sink)) {
			return
		}
	}
}

// Volume represents the current audio volume and mute state.
type Volume struct {
	Min, Max, Vol int64
	Mute          bool
	controller    controller
	update        func(Volume)
}

// Frac returns the current volume as a fraction of the total range.
func (v Volume) Frac() float64 {
	return float64(v.Vol-v.Min) / float64(v.Max-v.Min)
}

// Pct returns the current volume in the range 0-100.
func (v Volume) Pct() int {
	return int((v.Frac() * 100) + 0.5)
}

// SetVolume sets the system volume.
// It does not change the mute status.
func (v Volume) SetVolume(volume int64) {
	if volume > v.Max {
		volume = v.Max
	}
	if volume < v.Min {
		volume = v.Min
	}
	if volume == v.Vol {
		return
	}
	if err := v.controller.setVolume(volume); err != nil {
		l.Log("Error updating volume: %v", err)
		return
	}
	v.Vol = volume
	v.update(v)
}

// SetMuted controls whether the system volume is muted.
func (v Volume) SetMuted(muted bool) {
	if v.Mute == muted {
		return
	}
	if err := v.controller.setMuted(muted); err != nil {
		l.Log("Error updating mute state: %v", err)
		return
	}
	v.Mute = muted
	v.update(v)
}

type controller interface {
	setVolume(int64) error
	setMuted(bool) error
}

// Interface that must be implemented by individual volume implementations.
type moduleImpl interface {
	// Infinite loop: push updates and errors to the provided ErrorValue.
	worker(s *value.ErrorValue)
}

// Module represents a bar.Module that displays volume information.
type Module struct {
	outputFunc value.Value // of func(Volume) bar.Output
	impl       moduleImpl
}

// Output configures a module to display the output of a user-defined
// function.
func (m *Module) Output(outputFunc func(Volume) bar.Output) *Module {
	m.outputFunc.Set(outputFunc)
	return m
}

// Throttle volume updates to once every ~20ms to avoid unexpected behaviour.
var rateLimiter = rate.NewLimiter(rate.Every(20*time.Millisecond), 1)

// defaultClickHandler provides a simple example of the click handler capabilities.
// It toggles mute on left click, and raises/lowers the volume on scroll.
func defaultClickHandler(v Volume) func(bar.Event) {
	return func(e bar.Event) {
		if !rateLimiter.Allow() {
			// Don't update the volume if it was updated <20ms ago.
			return
		}
		if e.Button == bar.ButtonLeft {
			v.SetMuted(!v.Mute)
			return
		}
		volStep := (v.Max - v.Min) / 100
		if volStep == 0 {
			volStep = 1
		}
		if e.Button == bar.ScrollUp {
			v.SetVolume(v.Vol + volStep)
		}
		if e.Button == bar.ScrollDown {
			v.SetVolume(v.Vol - volStep)
		}
	}
}

// Stream starts the module.
func (m *Module) Stream(s bar.Sink) {
	var vol value.ErrorValue

	v, err := vol.Get()
	nextV, done := vol.Subscribe()
	defer done()
	go m.impl.worker(&vol)

	outputFunc := m.outputFunc.Get().(func(Volume) bar.Output)
	nextOutputFunc, done := m.outputFunc.Subscribe()
	defer done()

	for {
		if err != nil {
			fmt.Println(err)
		}
		if s.Error(err) {
			return
		}
		if volume, ok := v.(Volume); ok {
			volume.update = func(v Volume) { vol.Set(v) }
			s.Output(outputs.Group(outputFunc(volume)).
				OnClick(defaultClickHandler(volume)))
		}
		select {
		case <-nextV:
			v, err = vol.Get()
		case <-nextOutputFunc:
			outputFunc = m.outputFunc.Get().(func(Volume) bar.Output)
		}
	}
}

// createModule creates a new module with the given backing implementation.
func createModule(impl moduleImpl) *Module {
	m := &Module{impl: impl}
	l.Register(m, "outputFunc", "impl")
	// Default output is just the volume %, "MUT" when muted.
	m.Output(func(v Volume) bar.Output {
		if v.Mute {
			return outputs.Text("MUT")
		}
		return outputs.Textf("%d%%", v.Pct())
	})
	return m
}
