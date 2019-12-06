# barry

Barry is a custom i3 bar made with [barista](http://barista.run).

It has the following features:

- Spotify controls
- Volume control through `pulseaudio` (requires dbus protocol module)
- Brightness status using `light`
- Free memory
- Free disk space on /home
- CPU load
- Battery status
- Date
- Time

### Screenshot

![barry](/screenshot.png)

### Install

```
go get github.com/erizocosmico/barry
mkdir -p ~/.barry
go build -o ~/.barry/barry github.com/erizocosmico/barry
```

Then add this to your i3 config.

```
bar {
    status_command ~/.barry/barry
}
```

### License

Apache 2.0, see [LICENSE](/LICENSE)