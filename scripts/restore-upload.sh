SUDO=""; [ "$(id -u)" = 0 ] || SUDO="sudo -n"; $SUDO sh -c 'umask 077; mkdir -p "$1" && cat > "$1/copy.tar.gz"' restore "$DIR/$RESTORE"
