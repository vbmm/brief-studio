#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."
version="${BRIEF_VERSION:-0.1.0}"
repo="${BRIEF_RELEASE_REPOSITORY-vbmm/brief-studio}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo 'BRIEF_VERSION doit être au format 1.2.3.' >&2
    exit 2
fi
if [[ -n "$repo" && ! "$repo" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ ]]; then
    echo 'BRIEF_RELEASE_REPOSITORY doit être au format propriétaire/dépôt.' >&2
    exit 2
fi
if [[ -n "${BRIEF_NOTARY_PROFILE:-}" && -z "${BRIEF_SIGN_IDENTITY:-}" ]]; then
    echo 'La notarisation nécessite BRIEF_SIGN_IDENTITY (Developer ID Application).' >&2
    exit 2
fi

compile=(swift build --configuration release)
if [[ "${BRIEF_UNIVERSAL:-0}" == 1 ]]; then
    compile+=(--arch arm64 --arch x86_64)
fi
"${compile[@]}"
binary_dir="$("${compile[@]}" --show-bin-path)"

out="${BRIEF_BUILD_OUTPUT:-$PWD/dist}"
mkdir -p "$out"
out="$(cd "$out" && pwd -P)"
scratch="$(mktemp -d)"
trap 'rm -r "$scratch"' EXIT
app="$scratch/Brief Studio.app"
contents="$app/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources"
cp "$binary_dir/BriefStudio" "$contents/MacOS/BriefStudio"
{
    cat viewer/index.html
    printf '\n<!--\n'
    cat LICENSE
    printf '%s\n' '-->'
} > "$contents/Resources/brief-viewer.html"
cp LICENSE "$contents/Resources/LICENSE"
swift packaging/icon.swift "$scratch/BriefStudio.iconset"
iconutil -c icns "$scratch/BriefStudio.iconset" -o "$contents/Resources/BriefStudio.icns"

plist="$contents/Info.plist"
plutil -create xml1 "$plist"
plutil -insert CFBundleName -string 'Brief Studio' "$plist"
plutil -insert CFBundleDisplayName -string 'Brief Studio' "$plist"
plutil -insert CFBundleIdentifier -string 'app.briefstudio.macos' "$plist"
plutil -insert CFBundlePackageType -string 'APPL' "$plist"
plutil -insert CFBundleExecutable -string 'BriefStudio' "$plist"
plutil -insert CFBundleIconFile -string 'BriefStudio.icns' "$plist"
plutil -insert CFBundleShortVersionString -string "$version" "$plist"
plutil -insert CFBundleVersion -string "$version" "$plist"
plutil -insert LSMinimumSystemVersion -string '14.0' "$plist"
plutil -insert NSHighResolutionCapable -bool YES "$plist"
plutil -insert BriefReleaseRepository -string "$repo" "$plist"

if [[ -n "${BRIEF_SIGN_IDENTITY:-}" ]]; then
    codesign --sign "$BRIEF_SIGN_IDENTITY" --force --options runtime --timestamp "$app"
else
    codesign --sign - --force "$app"
fi
codesign --verify --strict "$app"

# Notarization executes only with an explicitly configured Apple profile.
if [[ -n "${BRIEF_NOTARY_PROFILE:-}" ]]; then
    ditto -c -k --keepParent "$app" "$scratch/notary.zip"
    xcrun notarytool submit "$scratch/notary.zip" --keychain-profile "$BRIEF_NOTARY_PROFILE" --wait
    xcrun stapler staple "$app"
    xcrun stapler validate "$app"
fi

final_app="$out/Brief Studio.app"
final_zip="$out/Brief-Studio-$version-macOS.zip"
for target in "$final_app" "$final_zip" "$final_zip.sha256" "$out/Exemple-brief-NQ-ES.html" "$out/Lecteur-brief.html"; do
    if [[ -e "$target" || -L "$target" ]]; then
        echo 'Utilise un nouveau dossier BRIEF_BUILD_OUTPUT pour conserver le build précédent.' >&2
        exit 2
    fi
done
ditto "$app" "$final_app"
ditto -c -k --sequesterRsrc --keepParent "$final_app" "$final_zip"
(cd "$out" && shasum -a 256 "$(basename "$final_zip")") > "$final_zip.sha256"
"$final_app/Contents/MacOS/BriefStudio" --export-example "$out/Exemple-brief-NQ-ES.html"
cp "$contents/Resources/brief-viewer.html" "$out/Lecteur-brief.html"
printf 'App : %s\nArchive : %s\n' "$final_app" "$final_zip"
