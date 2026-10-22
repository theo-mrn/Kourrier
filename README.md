# Kourrier

Microservice d'e-mailing **asynchrone**, **cloud-native** et **agnostique du fournisseur**, écrit en Go.

Vos applications font un `POST`, reçoivent un `202` en quelques millisecondes, et Kourrier se charge du reste : rendu des templates stockés sur S3, respect du quota d'envoi, retries avec backoff, dead-letter queue, métriques et dashboard Grafana.

```
POST /v1/emails ──► API ──► Redis Streams ──► Workers ──► AWS SES / SMTP
                     │                          │
                   202 Accepted          templates S3 (cache)
```

## Fonctionnalités

- **Asynchrone et fiable** : Redis Streams avec consumer groups, livraison au moins une fois, reprise des jobs d'un Pod mort, retries avec backoff exponentiel, dead-letter queue rejouable.
- **Agnostique** : drivers `aws-ses`, `smtp`, `console`, `filesystem` derrière une interface unique. Le stockage des templates accepte S3, MinIO, RustFS ou un dossier local.
- **Sûr par défaut** : le driver par défaut n'envoie rien. Clés API par tenant (seules les empreintes SHA-256 sont stockées), expéditeurs autorisés, en-têtes filtrés, échappement HTML automatique.
- **Idempotent** : l'en-tête `Idempotency-Key` évite les doublons quand un client renvoie sa requête.
- **Rate limiting distribué** (GCRA sur Redis) : le quota SES est respecté quel que soit le nombre de replicas.
- **Observabilité native** : métriques Prometheus, logs JSON (adresses masquées), dashboard Grafana et alertes livrés dans le Chart Helm.
- **Léger** : un binaire, environ 11 Mio de RAM, image distroless non-root d'environ 44 Mo.

## Démarrage rapide

```sh
make up      # Redis, RustFS (S3), Mailpit et Kourrier via Docker Compose
make send    # envoie l'e-mail de démonstration "welcome"
```

Ouvrez ensuite Mailpit sur <http://localhost:8025> pour voir l'e-mail.

Les ports hôtes sont surchargeables en cas de conflit, par exemple `REDIS_PORT=6380 MAILPIT_PORT=8026 make up`.

Sans Docker, avec un Redis local, `make run` sert les templates de `examples/templates` et affiche les e-mails dans le terminal.

## Envoyer un e-mail

```sh
curl -X POST http://localhost:8080/v1/emails \
  -H "Authorization: Bearer kr_dev_local_insecure_key" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: 6f1c2a7e-signup-42" \
  -d '{
        "to": [{ "email": "jane@example.com", "name": "Jane" }],
        "template": {
          "name": "welcome",
          "locale": "fr",
          "data": { "name": "Jane", "login_url": "https://example.com/login" }
        }
      }'
# {"id":"0192f1b4-…","status":"queued"}

curl -H "Authorization: Bearer kr_dev_local_insecure_key" http://localhost:8080/v1/emails/0192f1b4-…
# {"id":"0192f1b4-…","status":"sent","attempts":1,…}
```

La référence complète se trouve dans [docs/api.md](docs/api.md).

## Templates

Organisation dans le bucket :

```
<tenant>/_layout.html                       layout HTML partagé (optionnel)
<tenant>/<template>/<locale>/subject.txt
<tenant>/<template>/<locale>/body.html      html/template
<tenant>/<template>/<locale>/body.txt       text/template
```

Un exemple complet se trouve dans [examples/templates](examples/templates/demo). Les règles détaillées sont dans l'[ADR 010](docs/adr/0010-format-des-templates.md).

## Déploiement sur Kubernetes

```sh
kourrier keygen   # génère une clé API et son empreinte pour tenants.yaml

helm install kourrier deploy/helm/kourrier \
  --set mailer.provider=aws-ses \
  --set mailer.ses.region=eu-west-3 \
  --set redis.addr=redis-master:6379 \
  --set storage.s3.bucket=my-templates \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/kourrier \
  --set serviceMonitor.enabled=true \
  --set prometheusRule.enabled=true \
  -f my-tenants.yaml
```

Le chart fournit :
- un Deployment durci (non-root, rootfs en lecture seule, seccomp) ;
- des probes et un arrêt propre ;
- un Secret pour les tenants, avec checksum pour redémarrer les Pods à chaque modification ;
- les ressources optionnelles suivantes : Ingress, HPA, PDB, NetworkPolicy, ServiceMonitor et PrometheusRule ;
- le **dashboard Grafana** (ConfigMap portant le label `grafana_dashboard`).

Toutes les options sont décrites dans [values.yaml](deploy/helm/kourrier/values.yaml) et [docs/configuration.md](docs/configuration.md).

## Documentation

- [Architecture](docs/architecture.md)
- [Décisions d'architecture (ADRs)](docs/adr/README.md)
- [API](docs/api.md)
- [Configuration](docs/configuration.md)
- [Exploitation (DLQ, rotation des clés, alertes)](docs/operations.md)

## Développement

```sh
make test        # tests avec le détecteur de race (Redis simulé par miniredis)
make lint        # go vet et golangci-lint
make build       # bin/kourrier
make helm-lint   # lint et rendu du chart
make help        # toutes les cibles
```

```
cmd/kourrier/          point d'entrée et CLI (serve, keygen, dlq, healthcheck)
internal/app/          racine de composition et cycle de vie
internal/api/          API HTTP et serveur d'administration
internal/queue/        file Redis Streams
internal/worker/       pool de workers
internal/templates/    rendu et cache des templates
internal/provider/     drivers d'envoi (ses, smtp, console, filesystem, memory)
internal/storage/      stockage des templates (s3store, fsstore)
deploy/helm/kourrier/  Chart Helm et dashboard Grafana
docs/                  architecture, ADRs, API, configuration, exploitation
```
