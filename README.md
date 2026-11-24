# Brief Studio

Brief Studio est une mini-app macOS pour générer ses propres briefs en français sur les futures NQ et ES. Le Mac collecte les données et l’utilisateur lance la génération avec son propre compte ChatGPT. L’export HTML s’ouvre dans un navigateur sur Windows ou macOS.

[Téléchargements et exemple HTML](https://github.com/vbmm/brief-studio/releases/latest) · [Licence MIT](LICENSE)

## Essayer

1. Ouvre **Brief Studio.app** et sélectionne **Exemple NQ / ES** pour voir la présentation.
2. Choisis **Nouveau brief**, puis **Continuer avec ChatGPT**. Le navigateur gère la connexion. Quand le compte autorise l’utilisation du forfait, choisis un modèle et lance **Générer**.
3. Sélectionne **Partager → Exporter pour Windows et navigateur**. Envoie le fichier HTML. Il se lit d’un double-clic et se suffit à lui-même.

Si le navigateur ne s’ouvre pas, **Copier le lien** dans la fenêtre de connexion permet de coller la demande directement dans la barre d’adresse du navigateur. Le lien expire au bout de trois minutes. Chaque nouveau build d’essai signé ad hoc peut demander une nouvelle connexion.

Le lecteur `viewer/index.html` accepte également un brief JSON ou Markdown local. Le fichier `Exemple-brief-NQ-ES.html` produit par le build contient des données fictives et peut servir de démonstration.

Le moteur récupère des cotations futures différées via Yahoo Finance, vérifie leur heure d’observation et vérifie la couverture de la séance US avant d’afficher son haut, son bas et sa clôture. Il collecte les titres récents de politique monétaire de la Fed. Tu peux joindre une note macro avec un lien de source. La lecture de chaque instrument repose sur ses propres données. Les niveaux sont calculés par le code. Le modèle choisit une lecture structurée. Le calendrier économique complet, les flux X et Discord, les sources sous abonnement et les mesures GEX appartiennent au pipeline privé. Vérifie les conditions des fournisseurs avant de redistribuer des données de marché. [Sources Fed](https://www.federalreserve.gov/feeds/feeds.htm)

## Données et compte

La connexion suit le parcours OpenAI destiné aux apps locales open source. Les autorisations et la session sont conservées dans le Trousseau de ce Mac. Quand tu cliques sur **Générer**, l’app transmet à OpenAI les observations résumées et la note saisie. Ton espace de travail et ton forfait déterminent l’accès et les limites. La bibliothèque est locale dans `~/Library/Application Support/Brief Studio/Briefs`. Un export comprend le contenu du brief et ses sources, y compris la note facultative. L’app permet de déconnecter le compte. Tu peux aussi révoquer l’accès depuis ChatGPT. [Connexion avec ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source) · [Gérer l’utilisation](https://chatgpt.com/settings/usage)

## Préparer une distribution

`swift test` exécute la suite. `packaging/build.sh` crée l’application, le ZIP, sa somme SHA-256 et les deux HTML. Le paquet vise macOS 14 ou plus récent. `BRIEF_UNIVERSAL=1` compile pour Apple Silicon et Intel. Le paquet local utilise une signature ad hoc. Pour la distribution normale, utilise un certificat Developer ID et la notarisation Apple.

Les builds utilisent `vbmm/brief-studio` pour les mises à jour. Un fork peut changer `BRIEF_RELEASE_REPOSITORY` au build. La version est lue dans `BRIEF_VERSION` au format `1.2.3`. L’app consulte GitHub Releases et ouvre la page de téléchargement si une version plus récente existe. L’utilisateur installe cette version. [API GitHub Releases](https://docs.github.com/en/rest/releases/releases#get-the-latest-release)

`BRIEF_SIGN_IDENTITY` sélectionne ton certificat Developer ID à partir du Trousseau. `BRIEF_NOTARY_PROFILE` active la notarisation via le profil Apple local choisi. La commande de build reste locale tant que ce profil est absent. Elle ne publie pas sur GitHub.

Le ZIP de la version 0.1.1 est un paquet d’essai avec une signature ad hoc. La connexion avec un vrai compte ChatGPT et la génération NQ/ES ont été vérifiées sur cette version. L’installation après notarisation et l’ouverture d’un export sur un poste Windows restent à vérifier.
