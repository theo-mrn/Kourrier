# ADR 007 : Idempotence des envois

**Statut** : Accepté

## Contexte

Une application cliente qui subit un timeout réseau ne sait pas si sa requête a été prise en compte. Si elle la renvoie, l'utilisateur final risque de recevoir l'e-mail deux fois.

## Décision

`POST /v1/emails` accepte un en-tête optionnel **`Idempotency-Key`** (1 à 255 caractères ASCII imprimables), sur le modèle de Stripe et du draft IETF *httpapi-idempotency-key-header*.

- La clé est réservée atomiquement (`SET NX GET`, TTL de 24 h par défaut), avec un espace de noms par tenant.
- Une seconde requête avec la même clé et le **même corps** reçoit la réponse d'origine (même `id`, en-tête `Idempotent-Replayed: true`), sans nouvelle mise en file.
- La même clé avec un **corps différent** renvoie `422`, pour éviter qu'une erreur côté client passe inaperçue.
- Si la mise en file échoue après la réservation, la clé est libérée : le client peut réessayer.
- L'empreinte du corps est un SHA-256 du JSON canonique (clés triées).

## Conséquences

- **Positif** : les clients peuvent retenter sans risque. Combinée à l'[ADR 006](0006-fiabilite-redis-streams.md), elle donne une protection de bout en bout contre les doublons.
- **Négatif** : la clé reste facultative. Un client qui ne l'envoie pas n'est pas protégé, ce que la documentation doit recommander clairement.
