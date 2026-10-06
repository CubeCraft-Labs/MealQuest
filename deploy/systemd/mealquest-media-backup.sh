#!/bin/sh
# Nightly copy of the MealQuest media directory (recipe and dinner photos)
# to the Synology NAS. The database itself is replicated continuously by
# Litestream; this covers the files beside it.
#
# Runs as the unprivileged app user (10001, host user "mealquest"), never
# root: mealquest-media-backup.service bind-mounts the data volume
# read-only at /run/mealquest-data and hands the NAS key in as a systemd
# credential. Anything the app could plant in the volume is therefore read
# with the app's own permissions only.
#
# The script changes into the media directory and checks that the physical
# path is the expected one, so a media path that is a symlink is refused.
# rsync then copies "./" — the directory the shell is already in — so
# swapping the path afterwards cannot redirect it. Symlinks inside the
# directory are copied as links (-a), never followed.
#
# The media directory is created at deploy time (deploy/RUNBOOK.md). If it
# is missing the job fails; it is never treated as "nothing to back up".
#
# rsync mirrors into .../mealquest-backups/media/ only: the target ends in
# a slash, so --delete removes files from that directory and nothing
# outside it. Re-runs transfer only what changed. Output goes to the
# journal via mealquest-media-backup.service.
set -eu

src=/run/mealquest-data/media
dest=joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/
key=$CREDENTIALS_DIRECTORY/nas_key
known_hosts=/etc/mealquest/known_hosts

if ! cd "$src"; then
	echo "mealquest-media-backup: media directory $src is missing" >&2
	exit 1
fi
if [ "$(pwd -P)" != "$src" ]; then
	echo "mealquest-media-backup: $src is a symlink to $(pwd -P); refusing to back it up" >&2
	exit 1
fi

rsync -a --delete --stats \
	-e "ssh -F none -i $key -o IdentitiesOnly=yes -o BatchMode=yes -o UserKnownHostsFile=$known_hosts -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=yes" \
	./ "$dest"
echo "mealquest-media-backup: synced $src to $dest as uid $(id -u)"
