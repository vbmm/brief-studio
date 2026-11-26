# Brief Studio

Brief Studio génère des briefs en français sur les futures NQ et ES avec ton compte ChatGPT. La version web locale fonctionne depuis un navigateur sur macOS et Windows. Chaque brief peut aussi être exporté en HTML autonome pour la lecture hors ligne.

[Télécharger Brief Studio Web 0.3.0](https://github.com/vbmm/brief-studio/releases/tag/v0.3.0) · [Voir toutes les versions](https://github.com/vbmm/brief-studio/releases) · [Licence MIT](LICENSE)

## Lancer la version web locale

1. Décompresse le ZIP correspondant à ton système et à ton processeur. Sur Windows, ouvre **Brief-Studio-Web.exe**. Sur Mac, ouvre **Ouvrir-Brief-Studio.command**.
2. Garde la fenêtre de lancement ouverte. Ton navigateur affiche Brief Studio. Si besoin, ouvre l’adresse indiquée dans la fenêtre de lancement.
3. Choisis **Nouveau brief**, puis **Continuer avec ChatGPT**. L’autorisation et le retour à Brief Studio se font dans le même onglet. Choisis un modèle parmi ceux autorisés pour ton compte, puis clique sur **Générer**.

Les paquets web intègrent le serveur, le lecteur et le moteur. Leur lancement ne demande ni Go, ni Python, ni Codex. La connexion à ChatGPT et la génération nécessitent Internet. Le serveur écoute sur `127.0.0.1` uniquement. La session ChatGPT reste en mémoire et prend fin à l’arrêt du serveur. Les briefs restent dans `%LOCALAPPDATA%\Brief Studio Web\briefs` sur Windows et `~/Library/Application Support/Brief Studio Web/briefs` sur Mac. Utilise **Exporter en HTML** pour obtenir un fichier lisible sur un autre ordinateur.

Le brief de base utilise les cotations différées NQ/ES de Yahoo Finance, les communiqués de la Fed et le **Calendrier économique US**. Le calendrier affiche les horaires annoncés sur 14 jours à compter de la date à New York. La collecte publique ne demande aucune clé. Quand le BLS est indisponible, l’app peut utiliser son calendrier relayé par The Open Count si la copie est récente. [BLS](https://www.bls.gov/help/hlpiCAL.htm) · [BEA](https://www.bea.gov/news/schedule/full) · [Fed](https://www.federalreserve.gov/newsevents/calendar.htm) · [The Open Count](https://theopencount.com/calendar/)

### Connecter ses sources personnelles

Dans **Sources**, configure chaque service séparément. **Enregistrer** active la source. **Tester** effectue une lecture et indique le nombre de résultats récents. **Déconnecter** efface les paramètres de cette source.

- **Discord.** Dans le portail développeur, crée une application et utilise le jeton de son bot. Un administrateur doit ajouter le bot au serveur et lui donner **Voir le salon** et **Voir les anciens messages** sur le salon visé. Active l’accès au contenu des messages pour l’application si ton bot peut en bénéficier. Dans Brief Studio, saisis le lien ou l’identifiant du salon et le jeton du bot. Pour la génération, sélectionne un salon dont tu peux transmettre des extraits à OpenAI. Le brief et son export conservent les permaliens des messages. [Portail Discord](https://discord.com/developers/applications) · [Permissions](https://docs.discord.com/developers/resources/message#get-channel-messages) · [Accès au contenu](https://docs.discord.com/developers/events/gateway#message-content-intent)
- **X.** Dans la console développeur X, utilise le jeton **Bearer** de ton application ayant accès à la recherche récente. Saisis entre un et cinq comptes publics séparés par des virgules, par exemple `@deitaone`. Chaque test et génération lit au plus 25 publications en une recherche. X peut débiter des crédits pour ces lectures. Le brief ajoute les liens d’origine et garde le texte des publications hors des requêtes au modèle et des exports. [Console développeur X](https://developer.x.com/en/portal/dashboard) · [Recherche récente](https://docs.x.com/x-api/posts/search/quickstart/recent-search) · [Tarification](https://docs.x.com/x-api/getting-started/pricing)

Le jeton reste en mémoire tant que la fenêtre de lancement tourne. **Garder le jeton sur cet ordinateur** l’enregistre dans un fichier local du profil utilisateur, en clair pour une utilisation au prochain lancement. Garde ce dossier privé. La bibliothèque enregistre les briefs et leurs liens. Les jetons ne sont jamais transmis à ChatGPT ni inclus dans un export.

Pour lancer depuis le code source avec Go 1.23 ou ultérieur, exécute `go run ./web`. L’option `--no-browser` affiche uniquement l’adresse et `--data-dir` permet de choisir le dossier de stockage. `go test ./...` vérifie le moteur et le serveur. `packaging/build-web.sh` construit les ZIP macOS et Windows x64 et ARM64 et leurs sommes SHA-256 dans `dist/web-<version>/`. Les paquets de test macOS sont distribués sans notarisation. Si macOS bloque l’exécutable téléchargé, tu peux utiliser le lancement depuis le code source. **Mises à jour** vérifie les paquets web stables sur GitHub Releases. Les versions d’essai se téléchargent depuis la page des versions.

## Lancer l’app native historique sur Mac

1. Ouvre **Brief Studio.app** et sélectionne **Exemple NQ / ES** pour voir la présentation.
2. Choisis **Nouveau brief**, puis **Continuer avec ChatGPT**. Le navigateur gère la connexion. Quand le compte autorise l’utilisation du forfait, choisis un modèle et lance **Générer**.
3. Sélectionne **Partager → Exporter pour Windows et navigateur**. Envoie le fichier HTML. Il se lit d’un double-clic et se suffit à lui-même.

Si aucun onglet ne s’ouvre, **Copier le lien** dans la fenêtre de connexion permet de coller la demande directement dans la barre d’adresse. Le lien expire au bout de trois minutes. Chaque nouveau build d’essai signé ad hoc peut demander une nouvelle connexion.

Le lecteur `viewer/index.html` accepte également un brief JSON ou Markdown local. Le fichier `Exemple-brief-NQ-ES.html` produit par le build contient des données fictives et peut servir de démonstration.

L’app native collecte les cotations futures différées de Yahoo Finance et les titres monétaires de la Fed. Elle permet de joindre une note macro et un lien. Les connecteurs personnels et le calendrier économique se configurent dans la version web. [Sources Fed](https://www.federalreserve.gov/feeds/feeds.htm)

## Données et compte dans l’app native

La connexion suit le parcours OpenAI destiné aux apps locales open source. Les autorisations et la session sont conservées dans le Trousseau de ce Mac. Quand tu cliques sur **Générer**, l’app transmet à OpenAI les observations résumées et la note saisie. Ton espace de travail et ton forfait déterminent l’accès et les limites. La bibliothèque est locale dans `~/Library/Application Support/Brief Studio/Briefs`. Un export comprend le contenu du brief et ses sources, y compris la note facultative. L’app permet de déconnecter le compte. Tu peux aussi révoquer l’accès depuis ChatGPT. [Connexion avec ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source) · [Gérer l’utilisation](https://chatgpt.com/settings/usage)

## Préparer une distribution de l’app native

`swift test` exécute la suite. `packaging/build.sh` crée l’application, le ZIP, sa somme SHA-256 et les deux HTML. Le paquet vise macOS 14 ou plus récent. `BRIEF_UNIVERSAL=1` compile pour Apple Silicon et Intel. Le paquet local utilise une signature ad hoc. Pour la distribution normale, utilise un certificat Developer ID et la notarisation Apple.

Les builds utilisent `vbmm/brief-studio` pour les mises à jour. Un fork peut changer `BRIEF_RELEASE_REPOSITORY` au build. La version est lue dans `BRIEF_VERSION` au format `1.2.3`. L’app consulte GitHub Releases et ouvre la page de téléchargement si une version plus récente existe. L’utilisateur installe cette version. [API GitHub Releases](https://docs.github.com/en/rest/releases/releases#get-the-latest-release)

`BRIEF_SIGN_IDENTITY` sélectionne ton certificat Developer ID à partir du Trousseau. `BRIEF_NOTARY_PROFILE` active la notarisation via le profil Apple local choisi. La commande de build reste locale tant que ce profil est absent. Elle ne publie pas sur GitHub.

Le ZIP de la version 0.1.1 est un paquet d’essai avec une signature ad hoc. La connexion avec un vrai compte ChatGPT et la génération NQ/ES ont été vérifiées sur cette version. L’installation après notarisation et l’ouverture d’un export sur un poste Windows restent à vérifier.
