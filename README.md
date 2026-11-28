# Brief Studio

Brief Studio génère des briefs en français sur les futures NQ et ES avec ton compte ChatGPT. La version web locale fonctionne depuis un navigateur sur macOS et Windows. Chaque brief peut aussi être exporté en HTML autonome pour la lecture hors ligne.

[Télécharger la dernière version stable](https://github.com/vbmm/brief-studio/releases/latest) · [Voir toutes les versions](https://github.com/vbmm/brief-studio/releases) · [Licence MIT](LICENSE)

## Lancer la version web locale

Les ZIP Mac ciblent macOS 12 Monterey et les versions ultérieures. Choisis ARM64 sur Apple Silicon et x64 sur Mac Intel.

1. Décompresse le ZIP correspondant à ton système et à ton processeur. Sur Windows, ouvre **Brief-Studio-Web.exe**. Sur Mac, ouvre **Ouvrir-Brief-Studio.command**.
2. Garde la fenêtre de lancement ouverte. Ton navigateur affiche Brief Studio. Si besoin, ouvre l’adresse indiquée dans la fenêtre de lancement.
3. En séance, choisis **Nouveau brief**, puis **Continuer avec ChatGPT**. L’autorisation et le retour à Brief Studio se font dans le même onglet. Choisis un modèle parmi ceux autorisés pour ton compte, puis clique sur **Générer**.

Pendant une fermeture identifiée des futures NQ et ES, **Nouveau brief** propose **Générer la synthèse**. La synthèse factuelle date les dernières cotations vérifiées, indique la référence RTH disponible et s’exporte en HTML. Elle est calculée localement à partir des données publiques et des notes saisies. La 0.3.8 reconnaît les fermetures régulières et le créneau Thanksgiving vérifié du 26 novembre 2026. Durant les autres horaires spéciaux et en séance, le contrôle de fraîcheur habituel s’applique.

Les paquets web intègrent le serveur, le lecteur et le moteur. Leur lancement ne demande ni Go, ni Python, ni Codex. La connexion à ChatGPT et la génération nécessitent Internet. Le serveur écoute sur `127.0.0.1` uniquement. Les briefs restent dans `%LOCALAPPDATA%\Brief Studio Web\briefs` sur Windows et `~/Library/Application Support/Brief Studio Web/briefs` sur Mac. Utilise **Exporter en HTML** pour obtenir un fichier lisible sur un autre ordinateur.

**Compte ChatGPT** indique si la connexion est mémorisée, temporaire ou à renouveler. Une autorisation renouvelable est stockée dans le Trousseau macOS ou protégée par DPAPI sous Windows. Si la sauvegarde échoue et que le coffre a pu être ouvert, **Ressayer la sauvegarde** réessaie pour la session en cours. Une nouvelle version Mac signée ad hoc dispose de sa propre entrée de Trousseau. **Déconnecter** efface la session de cette version.

### Planifier un brief

Ouvre **Planification** après avoir connecté ton compte. Choisis l’heure, le fuseau, les jours, le modèle et le mode GEX. Autorise l’utilisation automatique de ton forfait ChatGPT et des sources actives, puis clique sur **Enregistrer et activer**. La planification est désactivée au premier lancement. **Mettre en pause** suspend les prochains départs. La déconnexion retire l’autorisation de cette planification. [Conditions de connexion ChatGPT](https://openai.com/policies/sign-in-with-chatgpt-terms/)

Laisse le lanceur ouvert et l’ordinateur éveillé à l’heure choisie. Le navigateur peut être fermé. Le dernier résultat et le prochain départ sont affichés dans **Planification**. Les briefs sont enregistrés dans la bibliothèque de l’app. Les créneaux passés de plus de cinq minutes sont ignorés. Le modèle doit rester disponible sur ton compte.

Le brief de base utilise les cotations différées NQ/ES de Yahoo Finance, les communiqués de la Fed et le **Calendrier économique US**. Le calendrier affiche les horaires annoncés sur 14 jours à compter de la date à New York. La collecte publique ne demande aucune clé. Quand le BLS est indisponible, l’app peut utiliser son calendrier relayé par The Open Count si la copie est récente. [BLS](https://www.bls.gov/help/hlpiCAL.htm) · [BEA](https://www.bea.gov/news/schedule/full) · [Fed](https://www.federalreserve.gov/newsevents/calendar.htm) · [The Open Count](https://theopencount.com/calendar/)

### Utiliser ses propres données GEX et niveaux futures

Dans **Nouveau brief**, choisis le mode adapté au relevé disponible.

- **Sans GEX**, le mode par défaut. Le brief ignore le gamma. Les niveaux récents de profil ou VWAP restent utilisables.
- **Afficher le GEX public**. Brief Studio interroge l’API publique de FlashAlpha pour les options sur les futures CME NQ et ES. Le relevé différé s’affiche localement s’il est disponible. En cas d’indisponibilité, le brief est généré et le panneau indique l’absence du relevé. Ouvre **Sources → Tester le GEX public** pour vérifier la couverture. [Documentation FlashAlpha](https://flashalpha.com/docs/api-specification) · [Conditions FlashAlpha](https://flashalpha.com/terms)
- **Avec GEX importé**. Un relevé gamma récent pour au moins un contrat est requis. Tu peux importer NQ, ES ou les deux. Le brief indique la couverture de chaque contrat. Le signe du gamma sert de contexte et ne valide pas une direction à lui seul.

Dans **Sources → Niveaux et GEX**, télécharge le modèle JSON si tu utilises tes propres données. Renseigne les valeurs de ton outil et importe le fichier.

Le format `brief-studio/market-input/v1` exige le nom de l’outil (`source`), l’heure réelle d’observation (`observedAt` en RFC 3339) et un objet `instruments` avec une ou deux clés `NQ` et `ES`. Chaque entrée contient le symbole correspondant `NQ=F` ou `ES=F` et au moins un objet `gamma` ou `levels`. Les champs d’un objet sont facultatifs, avec au moins une valeur renseignée.

| Objet | Champs |
| --- | --- |
| `gamma` | `regime` (`positive`, `negative`, `neutral`), `flip`, `callWall`, `putWall` |
| `levels` | `poc`, `val`, `vah`, `vwap` |

Exprime les niveaux en points du future correspondant à la série utilisée par Brief Studio. Ton outil doit assurer la correspondance de l’échéance et la conversion éventuelle depuis un indice ou un ETF avant export. La fenêtre de fraîcheur est de 24 heures. La date du modèle JSON est volontairement ancienne. Renseigne l’heure réelle du relevé. Si le fichier combine plusieurs observations, indique la plus ancienne d’entre elles.

L’import remplace le dernier instantané de ce profil. Un outil local peut aussi produire le même JSON et remplacer `market-input.json` dans le dossier indiqué par `--data-dir`. Écris d’abord dans un fichier temporaire du même dossier, puis remplace le fichier en une opération. Brief Studio relit le fichier à chaque génération.

Les champs retenus de ton fichier sont ajoutés au contexte envoyé à OpenAI. Le nom de l’outil et l’heure restent affichés dans le brief et son export HTML. Le champ facultatif `sourceURL` ajoute un lien HTTPS de référence sans déclencher de requête de l’app. Laisse les clés et jetons dans ton outil.

### Connecter ses sources personnelles

Dans **Sources**, configure chaque service séparément. **Enregistrer** active la source. **Tester** effectue une lecture et indique le nombre de résultats récents. **Déconnecter** efface les paramètres de cette source.

- **Discord.** Dans le portail développeur, crée une application et utilise le jeton de son bot. Un administrateur doit ajouter le bot au serveur et lui donner **Voir le salon** et **Voir les anciens messages** sur le salon visé. Active l’accès au contenu des messages pour l’application si ton bot peut en bénéficier. Dans Brief Studio, saisis le lien ou l’identifiant du salon et le jeton du bot. Pour la génération, sélectionne un salon dont tu peux transmettre des extraits à OpenAI. Le brief et son export conservent les permaliens des messages. [Portail Discord](https://discord.com/developers/applications) · [Permissions](https://docs.discord.com/developers/resources/message#get-channel-messages) · [Accès au contenu](https://docs.discord.com/developers/events/gateway#message-content-intent)
- **X.** Dans la console développeur X, utilise le jeton **Bearer** de ton application ayant accès à la recherche récente. Saisis entre un et cinq comptes publics séparés par des virgules, par exemple `@deitaone`. Chaque test et génération lit au plus 25 publications en une recherche. X peut débiter des crédits pour ces lectures. Le brief ajoute les liens d’origine et garde le texte des publications hors des requêtes au modèle et des exports. [Console développeur X](https://developer.x.com/en/portal/dashboard) · [Recherche récente](https://docs.x.com/x-api/posts/search/quickstart/recent-search) · [Tarification](https://docs.x.com/x-api/getting-started/pricing)

Si tu utilises déjà un collecteur X, **Sources → Importer un fichier X** accepte un instantané JSON local contenant les identifiants publics, les comptes et les dates. Les références de plus de 36 heures sont écartées. Le fichier prend la priorité sur l’API X tant qu’il est présent. Pour une installation qui possède déjà la base SQLite et le fichier de santé du collecteur du brief privé, `tools/export-x-snapshot.py` produit cet instantané en lecture seule, avec Python 3. Exemple depuis le dossier du dépôt.

```sh
python3 tools/export-x-snapshot.py --database /chemin/market-posts.db \
  --health /chemin/health.json --accounts deitaone --output x-snapshot.json
```

Sur Mac, **Sources → X → Actualiser Walter avec Bird** apparaît si la commande `bird` est installée. Le bouton lit les dernières publications publiques de `@deitaone` via le profil Chrome déjà configuré pour Bird. Brief Studio reçoit les identifiants et horodatages. Tu peux actualiser avant de générer le brief. Sur Windows, l’import du fichier JSON est disponible.

Avec ton compte Discord ouvert dans le navigateur, tu peux aussi sélectionner un message, copier son lien et coller un extrait dans **Nouveau brief → Contexte macro**, avec son permalien dans **Lien de la source**. [Copier un lien de message](https://support.discord.com/hc/en-us/articles/206346498-Where-can-I-find-my-User-Server-Message-ID)

Le jeton reste en mémoire tant que la fenêtre de lancement tourne. **Garder le jeton sur cet ordinateur** l’enregistre dans un fichier local du profil utilisateur, en clair pour une utilisation au prochain lancement. Garde ce dossier privé. La bibliothèque enregistre les briefs et leurs liens. Les jetons ne sont jamais transmis à ChatGPT ni inclus dans un export.

Pour lancer depuis le code source avec Go 1.23 ou ultérieur, exécute `go run ./web`. L’option `--no-browser` affiche uniquement l’adresse et `--data-dir` permet de choisir le dossier de stockage. `go test ./...` vérifie le moteur et le serveur. Sur Mac, `packaging/build-web.sh` utilise Go 1.23.x à 1.26.x pour construire les ZIP macOS et Windows x64 et ARM64 et leurs sommes SHA-256 dans `dist/web-<version>/`. Le script vérifie la cible macOS 12 dans les deux binaires Mac. Les ZIP macOS utilisent une signature ad hoc. Si macOS bloque l’exécutable téléchargé, tu peux utiliser le lancement depuis le code source. **Mises à jour → Vérifier** propose un lien vers GitHub quand une version Web stable plus récente est disponible. Le téléchargement et l’installation se font manuellement.

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
