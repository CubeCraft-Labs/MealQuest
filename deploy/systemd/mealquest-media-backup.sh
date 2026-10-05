#!/bin/sh
# Nightly copy of the MealQuest media directory (recipe and dinner photos)
# to the Synology NAS. The database itself is replicated continuously by
# Litestream; this covers the files beside it.
#
# rsync mirrors the media directory onto .../mealquest-backups/media/ only:
# both paths end in a slash, so --delete removes files from that target
# directory and nothing outside it. A missing source directory (no media
# uploaded yet) is reported and skipped, never synced, so an empty source
# can never wipe the remote copy. Re-runs transfer only what changed.
#
# The volume is writable by the app container, so a media path that is a
# symlink is refused: root's rsync would otherwise follow it and copy
# whatever it points at. Symlinks inside the directory are copied as links
# (-a), never followed.
#
# Connects with the dedicated MealQuest key; the NAS host key must already
# be in root's known_hosts (StrictHostKeyChecking=yes fails otherwise).
# Installed as /usr/local/sbin/mealquest-media-backup; output goes to the
# journal via mealquest-media-backup.service.
set -eu

src=/var/lib/docker/volumes/mealquest-data/_data/media/
dest=joshua@10.60.1.50:/volume1/homes/Joshua/mealquest-backups/media/
key=/home/overseer/mealquest/deploy/secrets/litestream_key

if [ -L "${src%/}" ]; then
	echo "mealquest-media-backup: ${src%/} is a symlink; refusing to back it up" >&2
	exit 1
fi

if [ ! -d "$src" ]; then
	echo "mealquest-media-backup: no media directory at $src yet; nothing to back up"
	exit 0
fi

rsync -a --delete --stats \
	-e "ssh -i $key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes" \
	"$src" "$dest"
echo "mealquest-media-backup: synced $src to $dest"
