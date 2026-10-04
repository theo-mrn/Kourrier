# ADR 001 : Choix du langage et du patron d'architecture (Go & monolithe modulaire)

**Statut** : Accepté

## Contexte

Le service doit être ultra-léger, capable de tourner sur de petits clusters Kubernetes ou des nœuds personnels sans consommer de ressources superflues, tout en restant propre et maintenable.

## Décision

Le microservice est développé en Go sous la forme d'un **monolithe modulaire** : un binaire unique, empaqueté dans un unique Pod Kubernetes. Le code est structuré en packages internes stricts (`internal/api`, `internal/worker`, `internal/provider`, `internal/storage`, …) qui partagent le même processus. Les dépendances entre packages passent par des interfaces déclarées côté consommateur ; `internal/app` est la seule racine de composition.

Le binaire accepte un mode d'exécution (`KOURRIER_MODE=all|api|worker`, `all` par défaut). Il ne remet pas en cause le monolithe : il permet simplement, le jour où le besoin apparaît, de scaler les workers indépendamment de l'API avec la même image.

## Conséquences

- **Positif** : empreinte mémoire minime (environ 11 Mio mesurés après 50 envois), démarrage instantané, concurrence native (goroutines), image distroless d'environ 44 Mo.
- **Négatif** : l'API et les workers partagent le même cycle de déploiement, ce qui suffit largement pour la cible visée.
