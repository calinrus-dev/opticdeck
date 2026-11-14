#!/bin/sh
set -eu

if [ ! -r /etc/os-release ]; then
    echo "Cannot identify distribution: /etc/os-release is missing" >&2
    exit 1
fi
. /etc/os-release

run_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    elif command -v doas >/dev/null 2>&1; then
        doas "$@"
    else
        echo "Run this script as root, or install sudo/doas." >&2
        exit 1
    fi
}

case "${ID:-} ${ID_LIKE:-}" in
    *debian*|*ubuntu*)
        run_root apt-get update
        gtk_package=libgtk-3-0
        if apt-cache show libgtk-3-0t64 >/dev/null 2>&1; then
            gtk_package=libgtk-3-0t64
        fi
        run_root apt-get install -y tigervnc-standalone-server openbox dbus-daemon dbus-x11 at-spi2-core x11-utils xfonts-base fonts-dejavu-core xdg-utils "$gtk_package"
        ;;
    *arch*)
        run_root pacman -S --needed tigervnc openbox dbus at-spi2-core xorg-xprop xorg-fonts-misc ttf-dejavu gtk3 xdg-utils
        ;;
    *fedora*|*rhel*|*centos*)
        run_root dnf install -y tigervnc-server openbox dbus-daemon dbus-x11 at-spi2-core xprop xorg-x11-fonts-misc dejavu-sans-fonts gtk3 xdg-utils
        ;;
    *)
        echo "Unsupported distribution: ${ID:-unknown}. Install TigerVNC Xvnc, openbox, dbus-daemon, at-spi2-core, xprop, and fonts manually." >&2
        exit 1
        ;;
esac

echo "Runtime dependencies installed. With Go >=1.23 and make available, run: make install"
