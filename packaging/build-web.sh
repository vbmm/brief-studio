#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
version=${BRIEF_VERSION:-0.3.7}
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    printf '%s\n' 'BRIEF_VERSION doit avoir le format 1.2.3.' >&2
    exit 1
fi

out="$PWD/dist/web-$version"
mkdir -p "$out"
staging=$(mktemp -d)
trap 'rm -r "$staging"' EXIT

for target in darwin-arm64 darwin-amd64 windows-amd64 windows-arm64; do
    platform=${target%-*}
    cpu=${target#*-}
    binary=Brief-Studio-Web
    launcher=$binary
    cgo=0
    case $platform in
        darwin)
            label=macOS
            launcher=Ouvrir-Brief-Studio.command
            cgo=1
            ;;
        windows)
            label=Windows
            binary=$binary.exe
            launcher=$binary
            ;;
    esac
    case $cpu in
        amd64) architecture=x64 ;;
        arm64) architecture=ARM64 ;;
    esac
    folder="$staging/Brief-Studio-Web"
    mkdir "$folder"
    CGO_ENABLED="$cgo" GOOS="$platform" GOARCH="$cpu" go build -trimpath \
        -ldflags="-s -w -X main.version=$version" -o "$folder/$binary" ./web
    if [ "$platform" = darwin ]; then
        /usr/bin/codesign --force --sign - --identifier com.briefstudio.web "$folder/$binary"
        /usr/bin/codesign --verify --strict "$folder/$binary"
        cat > "$folder/$launcher" <<'EOF'
#!/bin/sh
cd "$(dirname "$0")" || exit 1
exec ./Brief-Studio-Web "$@"
EOF
        chmod +x "$folder/$launcher"
    fi
    cp LICENSE "$folder/LICENSE"
    cat > "$folder/LISEZ-MOI.txt" <<EOF
Brief Studio Web $version · $label $architecture

1. Décompresse ce dossier, puis ouvre $launcher.
2. Ton navigateur affiche Brief Studio. Si besoin, copie l’adresse affichée
   dans la fenêtre de lancement dans la barre d’adresse du navigateur.
3. Choisis Nouveau brief, puis Continuer avec ChatGPT. La connexion revient
   dans le même onglet. Choisis un modèle et lance Générer.
4. Pour ajouter Discord ou X, ouvre Sources dans Brief Studio, renseigne tes
   accès, puis clique sur Enregistrer et Tester.
5. Dans Nouveau brief, choisis Avec GEX public pour lire le relevé différé
   de FlashAlpha dans l’app. Pour utiliser tes propres repères, importe
   le fichier dans Sources et choisis Avec GEX importé.
6. Dans Planification, choisis les jours, l’heure, le fuseau et le modèle.
   Autorise les générations automatiques avec ton forfait ChatGPT.
   Le lanceur doit rester ouvert et l’ordinateur éveillé.

Garde la fenêtre de lancement ouverte. Ferme-la pour arrêter l’app.
La connexion ChatGPT est conservée dans le Trousseau macOS ou protégée par
Windows pour ton compte utilisateur. Une nouvelle version Mac signée ad hoc,
une révocation ou une expiration peut nécessiter une nouvelle connexion.
Pour effacer la session, utilise Compte ChatGPT > Déconnecter.
La bibliothèque reste sur cet ordinateur. Un export HTML se lit hors ligne.

Les cotations sont différées. Une connexion Internet est nécessaire pour
la génération et la connexion à OpenAI.

Guide de configuration : https://github.com/vbmm/brief-studio#connecter-ses-sources-personnelles
EOF
    archive="Brief-Studio-$version-Web-$label-$architecture.zip"
    (cd "$staging" && zip -q -X -r "$archive" Brief-Studio-Web)
    mv "$staging/$archive" "$out/$archive"
    (cd "$out" && shasum -a 256 "$archive" > "$archive.sha256")
    rm -r "$folder"
    printf '%s\n' "$out/$archive"
done
