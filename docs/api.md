# API

Base : `http://<service>:8080`. Toutes les requêtes `/v1` exigent `Authorization: Bearer <clé API>`.

## `POST /v1/emails`

Met un e-mail en file et répond immédiatement.

**En-têtes**

| En-tête | |
|---------|---|
| `Authorization: Bearer kr_…` | Obligatoire. |
| `Idempotency-Key` | Recommandé. 1 à 255 caractères ASCII imprimables (ex. un UUID ou `order-42-confirmation`). Voir [ADR 007](adr/0007-idempotence.md). |
| `X-Request-ID` | Optionnel. Propagé dans les logs et renvoyé dans la réponse. |

**Corps, avec un template**

```json
{
  "from": { "email": "no-reply@yner.fr", "name": "Yner" },
  "to": [{ "email": "jane@example.com", "name": "Jane" }],
  "cc": [], "bcc": [], "reply_to": [{ "email": "support@yner.fr" }],
  "template": {
    "name": "welcome",
    "locale": "fr",
    "data": { "name": "Jane", "login_url": "https://yner.fr/login" }
  },
  "headers": { "List-Unsubscribe": "<https://yner.fr/unsubscribe/abc>" },
  "tags": { "campaign": "onboarding" }
}
```

**Corps, avec un contenu déjà rendu**

```json
{
  "to": [{ "email": "jane@example.com" }],
  "content": { "subject": "Votre code", "text": "Code : 123456", "html": "<p>Code : <b>123456</b></p>" }
}
```

Règles :

- `from` est facultatif (expéditeur par défaut du tenant) et doit faire partie des `allowed_senders`.
- Exactement un champ parmi `template` et `content`.
- 50 destinataires au maximum (`to` + `cc` + `bcc`, limite de SES).
- `headers` : uniquement `X-*`, `List-Unsubscribe`, `List-Unsubscribe-Post`, `List-Id` (20 au maximum).
- `tags` : 10 au maximum, clés et valeurs `[A-Za-z0-9_.-]{1,64}`. Ils sont transmis à SES (`EmailTags`) avec `kourrier_tenant` et `kourrier_template`.
- Corps limité à 1 Mio, champs inconnus refusés.

**Réponse `202 Accepted`**

```http
Location: /v1/emails/0192f1b4-6c1e-7a3b-8c4d-0123456789ab
Idempotent-Replayed: true        (uniquement en cas de rejeu)

{ "id": "0192f1b4-6c1e-7a3b-8c4d-0123456789ab", "status": "queued" }
```

Les identifiants sont des UUIDv7 (triés chronologiquement).

## `GET /v1/emails/{id}`

```json
{
  "id": "0192f1b4-6c1e-7a3b-8c4d-0123456789ab",
  "status": "sent",
  "attempts": 1,
  "provider": "aws-ses",
  "provider_message_id": "010f01…",
  "created_at": "2026-10-04T17:10:45.554Z",
  "updated_at": "2026-10-04T17:10:45.579Z"
}
```

`status` : `queued` → (`retrying` → …) → `sent` | `failed`. En cas de retry, `last_error` et `next_attempt_at` sont renseignés. Les statuts sont conservés 7 jours (`KOURRIER_STATUS_TTL`). Un ID inconnu ou appartenant à un autre tenant renvoie `404`.

## Erreurs

Les erreurs suivent la [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) (`application/problem+json`) :

```json
{
  "type": "https://github.com/theo-mrn/kourrier/blob/main/docs/api.md#validation",
  "title": "Invalid request",
  "status": 422,
  "detail": "the request body contains invalid fields",
  "request_id": "0192f1b4-…",
  "errors": [{ "field": "to[0].email", "message": "is not a valid e-mail address" }]
}
```

| Type | Statut | Signification |
|------|--------|---------------|
| <a id="malformed"></a>`malformed` | 400 | JSON invalide, champ inconnu ou données en trop. |
| <a id="idempotency-key"></a>`idempotency-key` | 400 | `Idempotency-Key` invalide. |
| <a id="unauthorized"></a>`unauthorized` | 401 | Clé API absente ou invalide. |
| <a id="sender-not-allowed"></a>`sender-not-allowed` | 403 | `from` hors des `allowed_senders` du tenant. |
| <a id="not-found"></a>`not-found` | 404 | Route ou e-mail inconnu. |
| <a id="too-large"></a>`too-large` | 413 | Corps supérieur à `KOURRIER_MAX_BODY_BYTES`. |
| <a id="validation"></a>`validation` | 422 | Champs invalides (détail dans `errors`). |
| <a id="idempotency-key-reused"></a>`idempotency-key-reused` | 422 | Clé déjà utilisée avec un corps différent. |
| <a id="internal"></a>`internal` | 500 | Erreur interne. |
| <a id="unavailable"></a>`unavailable` | 503 | Redis indisponible. Réessayer après `Retry-After` (avec la même `Idempotency-Key`). |

## Endpoints d'administration (port 9090)

| Endpoint | |
|----------|---|
| `GET /metrics` | Métriques Prometheus. |
| `GET /livez` | Liveness : le processus répond. |
| `GET /readyz` | Readiness : Redis joignable. |
