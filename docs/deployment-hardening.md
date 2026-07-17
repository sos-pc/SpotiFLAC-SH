# Durcissement du déploiement — compose, reverse proxy, conteneur

> **🔍 Constat — 2026-07-16.** Revue d'un `docker-compose.yaml` et d'une config SWAG/nginx réels, sur
> un déploiement de référence. **Vérifié par exécution** : sondes HTTP/2 depuis l'extérieur, tests de
> ports, lecture du code pour chaque affirmation.
> **Trois défauts corrigés, deux bugs applicatifs découverts au passage, un point ouvert.**
> Index : [README.md](README.md). Le durcissement ffmpeg est ailleurs :
> [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md).
>
> **Volontairement sans identifiants d'hôte.** Ce dépôt est public : les IP, noms de domaine, chemins
> de disque et clés du déploiement audité n'y ont pas leur place — associés à la liste de trous
> ouverts du §7, ils formeraient une feuille de route d'attaque plutôt que de la documentation. Les
> valeurs ci-dessous sont des marqueurs (`<ip-lan-hôte>`, `<réseau-swag>`…) ; les vraies vivent dans
> le compose de l'opérateur, pas ici. **Règle générale pour ce dossier : le contenu technique et les
> leçons se publient, l'identité d'un hôte précis ne se publie pas.**

---

## 0. Résumé

| # | Sujet | État |
|---|-------|------|
| 1 | **DoS login** — compteur de rate-limit partagé par tous | ✅ corrigé + vérifié |
| 2 | **Port 6890 ouvert sur le LAN** — contournait TLS et nginx | ✅ fermé + vérifié |
| 3 | Durcissement conteneur (`cap_drop`, `no-new-privileges`, `read_only`) | ✅ appliqué + **vérifié** (upload réel sous `read_only`, §5.1) |
| 4 | **`mem_limit` rejeté par le noyau** | 🔴 **ouvert** |
| 5 | Upload cassé derrière tout reverse proxy (bug applicatif) | ✅ corrigé (`8486c45`) + **vérifié en prod** |
| 6 | Flux SSE coupé toutes les 4 min (bug applicatif) | ✅ corrigé (`dcfa668`) + **vérifié en prod** (30,0 s mesurés) |
| 7 | Test instable + CI sans `-race` | 🟡 instabilité corrigée (`1a9dedc`), **`-race` : décision ouverte** |

---

## 1. La topologie réelle, et pourquoi elle comptait

SWAG (nginx dans un conteneur, sur son propre réseau Docker) joignait l'app par **l'IP LAN de
l'hôte** :

```nginx
set $upstream_app <ip-lan-hôte>;   # ← l'hôte, pas le conteneur
```

Or le template SWAG dit en tête du fichier : *« make sure that your spotiflac container is named
spotiflac »* — il est **conçu** pour résoudre le nom via le DNS Docker. La config avait dévié.

**Conséquence** : le port devait rester publié sur `0.0.0.0`, donc toute machine du LAN atteignait
`http://<ip-lan-hôte>:6890` — l'application entière, en clair, sans nginx ni TLS.

**Portée mesurée, pas supposée** : un test TCP sur le port 6890 de l'IP publique n'a donné
aucune connexion — seul le 443 est redirigé. L'exposition était donc **LAN uniquement**. Une première
formulation (« le TLS est décoratif ») était trop forte : il l'est pour un attaquant *déjà sur le
LAN*, ce qui est une hypothèse bien plus coûteuse.

## 2. Le DoS sur le login (le défaut sérieux)

`TRUST_PROXY_HEADERS` n'était pas défini. Sans lui, `remoteIP()` ignore `X-Forwarded-For` et renvoie
**l'IP du proxy pour toute requête externe** ([ratelimit.go:101](../ratelimit.go)).

Le rate-limiter de login est de **10 tentatives/minute puis blocage 5 minutes, par IP**
([ratelimit.go:13](../ratelimit.go)). Tous les utilisateurs d'internet partageaient donc **un seul
compteur** : 10 requêtes suffisaient à **bloquer les connexions de tout le monde**, indéfiniment, à
~2 req/min.

Ce n'est pas une faille d'authentification — le limiteur devient *plus* strict, pas plus laxiste.
C'est un déni de service à coût nul.

### La contrainte d'ordre, qui n'est pas cosmétique

Activer `TRUST_PROXY_HEADERS=true` **pendant que le port était publié** aurait été pire que le mal :
un client atteignant le conteneur en direct contrôle tout l'en-tête `X-Forwarded-For` et **échappe
complètement** au limiteur. On passe de « trop strict » à « inexistant ».

**D'abord fermer le port, ensuite faire confiance aux en-têtes.** Jamais l'inverse.

### Pourquoi `TRUST_PROXY_HEADERS=true` est correct ici — vérifié

Le `proxy.conf` de SWAG contient :

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

`$proxy_add_x_forwarded_for` **ajoute** l'IP réelle du client **à droite** de ce que le client a pu
envoyer. Le code lit le maillon **le plus à droite** ([ratelimit.go:113](../ratelimit.go)). Les deux
correspondent exactement : un client qui forge `X-Forwarded-For: 1.2.3.4` obtient
`1.2.3.4, <son-IP-réelle>`, et c'est son IP réelle qui est retenue. **Non forgeable via nginx.**

**Un seul saut de proxy, confirmé** : `curl -I` renvoie `Server: nginx`, aucun `cf-ray` — pas de
Cloudflare devant. S'il y en avait un, le maillon droit serait l'IP de SWAG et le compteur resterait
partagé.

## 3. Ce qui a été appliqué, et vérifié

```yaml
services:
  spotiflac:
    container_name: spotiflac          # nom résolu par nginx via le DNS Docker
    networks: [<réseau-swag>]           # le réseau Docker externe où tourne SWAG
    # aucun ports: — joignable uniquement via nginx
    stop_grace_period: 45s             # > les 30s de httpServer.Shutdown (main.go:170)
    environment:
      - TRUST_PROXY_HEADERS=true       # sûr UNE FOIS le port retiré
      - DISABLE_AUTH_ON_LAN=false
    user: "1000:1000"
    security_opt: [ "no-new-privileges:true" ]
    cap_drop: [ ALL ]                  # sûr : non-root, port 6890 > 1024
    read_only: true
    pids_limit: 512
    logging:
      driver: json-file
      options: { max-size: "10m", max-file: "3" }
    volumes:
      - <chemin-hôte>/Musique:/home/nonroot/Music
      - spotiflac_config:/home/nonroot/.SpotiFLAC
      - <chemin-hôte>/tmp:/tmp           # sur disque, pas tmpfs — voir §3.2
networks:
  <réseau-swag>: { external: true }
```

Côté nginx, une seule ligne : `set $upstream_app spotiflac;`.

**Vérification (pas déduction)** :

```
$ curl -m 5 http://<ip-lan-hôte>:6890      # depuis une autre machine du LAN
curl: (7) Failed to connect ... Couldn't connect to server
$ docker port spotiflac
(vide)
```

### 3.1 `stop_grace_period: 45s`, pas 30

[main.go:170](../main.go) fait `context.WithTimeout(ctx, 30*time.Second)` puis
`httpServer.Shutdown(ctx)`. Un `stop_grace_period: 30s` vaut **exactement** le timeout interne : si
l'arrêt prend les 30 s pleines, Docker envoie `SIGKILL` au moment même où l'app termine, possiblement
en pleine écriture BoltDB. 45 s garantit que l'app gagne la course.

### 3.2 `/tmp` sur disque, pas en tmpfs

`read_only: true` impose de monter `/tmp` quelque part : `handleUpload` écrit via
`os.CreateTemp(os.TempDir(), ...)` ([server.go:246](../server.go)).

Un tmpfs aurait été un mauvais choix, pour deux raisons **mesurées** :

- `client_max_body_size 500M` côté nginx, et `/api/upload` sert aux pages **Audio Analysis** et
  **Audio Converter** — donc de **vrais fichiers audio**, pas des playlists. 500 Mo est justifié.
- Le convertisseur téléverse **en boucle** (`for (const file of items)`), donc N fichiers cohabitent
  dans `/tmp`. Et **un tmpfs est décompté du `mem_limit`** : les uploads se paieraient en RAM.

Un bind mount sur disque supprime le dimensionnement comme problème. Le dossier doit être créé
**avant** en `1000:1000`, sinon Docker le crée root et l'app (uid 1000) ne peut pas y écrire — le
piège que [main.go:188](../main.go) documente déjà.

## 4. 🔴 OUVERT — `mem_limit` rejeté par le noyau

```
! spotiflac  Your kernel does not support memory limit capabilities
             or the cgroup is not mounted. Limitation discarded.
```

**La limite mémoire n'existe pas.** C'est le pire cas : une protection qu'on croit avoir. Et c'est
précisément celle qui bornerait un ffmpeg qui déraille sur un fichier forgé — le complément
*déploiement* du durcissement de [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md), qui ne
réduit pas la probabilité d'une RCE mais dont on voudrait borner l'épuisement de ressources, la
partie facile à déclencher.

`pids_limit` n'a **pas** été rejeté (aucun avertissement) — il est actif.

**Prochaine étape**, non faite : diagnostiquer avant de corriger.

```bash
grep memory /proc/cgroups     # enabled=0 en dernière colonne → contrôleur désactivé au boot
docker info 2>/dev/null | grep -iA3 WARNING
```

Cas classique sur OMV/Debian : ajouter `cgroup_enable=memory swapaccount=1` à
`GRUB_CMDLINE_LINUX_DEFAULT`, puis `update-grub` + reboot. **Hypothèse non vérifiée** — le
diagnostic doit précéder.

## 5. Deux bugs applicatifs trouvés en vérifiant le déploiement

Aucun des deux n'est une régression : tous deux dormaient depuis longtemps, invisibles.

### 5.1 `/api/upload` renvoyait toujours 401 (`8486c45`)

Les deux appelants court-circuitaient le helper `rest()` et faisaient un `fetch("/api/upload", {method,
body})` **sans le moindre en-tête**. Or `/api/upload` est protégé par `RequireAuth`
([server.go:147](../server.go)), qui exige `Authorization: Bearer`.

**Pourquoi ça a survécu si longtemps** : seul le bypass LAN le masquait — il injecte un JWT admin
synthétique, et `isLocalIP()` le désactive dès que `X-Forwarded-For` est présent. Donc l'upload
marchait **en accès direct sur le LAN et nulle part ailleurs**. Fermer le port (§2) est ce qui l'a
enfin rendu visible.

**Pourquoi l'échec était muet** : le corps d'un 401 est du JSON valide, donc `await res.json()`
parsait `{"error":"unauthorized"}` sans lever, `data.path` valait `undefined`, et le code ne faisait
**rien** — ni erreur affichée, ni ligne de log serveur (RequireAuth rejette avant tout handler).

Centralisé en `UploadFile()` dans `rpc.ts` : l'endpoint ne peut pas passer par `rest()` (il est hors
`/api/v1`) mais a besoin des mêmes `authHeaders()` et du même `401 → auth:expired`.

⚠️ **Non retesté en prod** — voir §7.

### 5.2 Le flux SSE mourait toutes les 4 minutes (`dcfa668`)

`v1JobsStream` n'écrivait que sur événement : un flux sans job actif restait **totalement muet**
après son `connected`. Tout reverse proxy coupe un amont silencieux — nginx le fait à
`proxy_read_timeout`, **240 s dans le proxy.conf SWAG de cette prod** (valeur lue, pas supposée).
EventSource reconnecte alors seul, le handler renvoie **tout le snapshot des jobs sur 48 h**, et le
cycle recommence toutes les 4 minutes tant qu'un onglet reste ouvert.

Rien n'était visiblement cassé — la reconnexion est transparente. Juste un scan BoltDB complet et une
re-sérialisation par client toutes les 4 min, pour afficher ce qui était déjà à l'écran.

Le keepalive est un **commentaire** SSE (`: keepalive`), pas un événement : la spec impose aux clients
d'ignorer ces lignes, donc rien n'atteint `onmessage`. 30 s laisse une marge large sous les 240 s.
`/api/v1/search/stream` n'en reçoit délibérément pas : il diffuse puis se termine.

## 6. Ce que la vérification a appris sur la méthode

**Deux affirmations fausses ont été faites puis corrigées en cours de session**, les deux du même
type — *déduire une propriété du serveur depuis une mesure non contrôlée* :

- « HTTP/2 n'est pas activé », d'après un `curl` qui renvoyait `http_version=1.1`. En réalité **le
  curl local ne supportait pas h2** (`the installed libcurl version does not support this`).
  `http2 on;` est bien dans le `nginx.conf`, et une sonde `httpx[http2]` renvoie `HTTP/2 200`.
  **Conséquence non triviale** : le correctif SSE `Connection: keep-alive` (`e3ce22e`) n'est pas de la
  préparation d'avenir — il est **actuellement porteur**.
- Un « test rouge » qui n'en était pas : stasher `sse.go` faisait échouer le test à la **compilation**
  (`undefined: sseHeartbeatInterval`). Une erreur de build ne prouve rien. Refait en gardant la
  variable et en neutralisant la seule émission, le rouge a alors montré le symptôme réel :
  `body="event: connected\ndata: {\"status\":\"ok\"}\n\n"` — rien après.

C'est la même famille d'erreur que le `FROM scratch` et le faux positif `libstdc++` de
[ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) : **une propriété affirmée sans mesure
contrôlée est une propriété fausse**, y compris quand l'instrument est en cause.

## 7. Tâches ouvertes

| # | Tâche | Pourquoi ça compte | Bloqué par |
|---|-------|--------------------|------------|
| 1 | **`mem_limit`** (§4) | protection absente qu'on croit avoir | diagnostic `/proc/cgroups` |
| 2 | **Révoquer la clé API admin utilisée pendant l'audit** | elle a été exposée en clair dans la conversation de travail ; une clé admin ne se périme pas toute seule (voir `authentication.md`) | — |
| 3 | Rotation du mot de passe Jellyfin de l'opérateur | exposé en clair dans un copier-coller de console navigateur — le champ mot de passe rend sa valeur dans l'attribut `value`, ce que les captures de console embarquent sans qu'on y pense | — |

### Clos par mesure le 2026-07-17

| Tâche | Comment on l'a su |
|-------|-------------------|
| **`-race` en CI** | La crainte (« ça peut révéler d'autres races et bloquer la CI ») a été **mesurée avant de décider** : une branche jetable `race-probe` a lancé toute la suite avec `-race` → **zéro race sur les 56 fichiers concurrents**, coût **+53 s** (76 s → 129 s), image non publiée (job `build-and-push` désactivé sur la branche). Activé sur `dev` ensuite : le risque était nul, le test `TestQueryConcurrentAccessIsRaceFree` fait enfin le travail qu'il documente. |

### Clos par mesure le 2026-07-16 (après redéploiement)

| Tâche | Comment on l'a su |
|-------|-------------------|
| **Upload sous `read_only`** | Un vrai fichier a été téléversé puis analysé : `/tmp/spotiflac_upload_<rand>_<nom>.flac`, 48 kHz / 24 bits / 46,9 Mo. Le chemin **prouve** que le bind mount disque monté sur `/tmp` est écrit par l'app en uid 1000 sous `read_only: true`. Ferme d'un coup trois inconnues : le correctif d'auth (`8486c45`), le montage, et `analysis.go` (ffprobe durci) sur un fichier de staging. |
| **Heartbeat SSE** | Sonde `httpx[http2]` sur `/api/v1/jobs/stream` à travers nginx : `: keepalive` reçu à 31,1 s puis 61,1 s → **intervalle mesuré 30,0 s**, très en-dessous des `proxy_read_timeout 240`. |

⚠️ **À ne pas confondre avec une panne** : `GET /api/v1/files/metadata?path=/tmp/spotiflac_upload_…`
renvoie `path ... is outside the configured library root`. **C'est correct.** `/files/metadata` passe
par `cleanLibraryPath` (confiné à la bibliothèque) alors que `/audio/analyze` passe par
`cleanUploadOrLibraryPath`, qui autorise en plus le staging d'upload. Distinction délibérée : c'est
le confinement de chemin qui fait son travail, pas un bug.

**Piège latent à ne pas réarmer** : `isLocalIP()` renvoie `true` pour un accès direct sans
`X-Forwarded-For`. Si `DISABLE_AUTH_ON_LAN` repassait un jour à `true` **et** qu'un port était
republié, toute machine du LAN obtiendrait un **JWT admin sans authentification**. Les deux
conditions sont fausses aujourd'hui ; c'est leur conjonction qui est dangereuse.
