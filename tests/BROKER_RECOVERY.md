# RabbitMQ persistence recovery check

This procedure recreates only a uniquely named test broker. A test-only Go helper declares a durable queue and publishes a persistent task through the production broker wrapper, which waits for publisher confirmation and rejects unroutable messages. The script recreates that broker with its original Compose hostname and named volume, then consumes the task after recreation. It checks the queue depth settles before restart and verifies the original body on consume. On success it removes only that test Compose project and its volume. If a step fails, it leaves that isolated project intact for inspection.

Run from the repository root with Docker Desktop using Linux containers and PowerShell 7:

```powershell
./tests/recover-broker.ps1
```

The script uses fixed fake credentials and no root `.env`, Telegram token, application container, or application volume. The test queue is unique to its project. It prints PASS only after the original message body is consumed following forced broker recreation. Review the generated project name if a failed run needs cleanup; use `docker compose -p <that-project> -f <its-temp-compose-file> down --volumes --remove-orphans` only for the named test project.

## Production RabbitMQ volume cutover

`docker-compose.yaml` assigns `rabbitmq_data` to `/var/lib/rabbitmq`; fresh installs use stable defaults `RABBITMQ_HOSTNAME=pricescount-rabbitmq` and `RABBITMQ_NODENAME=rabbit@pricescount-rabbitmq`. Both settings are configurable for restoration. Existing installations often have an anonymous volume attached to the old RabbitMQ container. Do not remove that container or volume to create the new one.

Before changing the broker mount, record the actual old node name (`rabbitmqctl eval 'node().'` inside the running old broker), image/version, configured `RABBITMQ_NODENAME`, hostname, mount source, and volume driver/options. RabbitMQ derives the Mnesia directory from the node name; a changed hostname or nodename can start an empty database and hide the old messages. Before starting the replacement with copied data, set BOTH `RABBITMQ_HOSTNAME` and `RABBITMQ_NODENAME` in `.env` to the exact recorded old hostname and node name; never infer or substitute these from the fresh-install defaults. If the old node used long names, also set `RABBITMQ_USE_LONGNAME=true` and verify on a rehearsal. Validate resolution with `docker compose --env-file <fake-validation-file> config rabbitmq` before using real values; this renders configuration and does not start services. A raw data-directory backup must be made from a stopped broker and restored with ownership, permissions, and the same RabbitMQ node name/version compatibility confirmed. An exported definitions file contains topology, not queued message contents; it is not a message backup. If node name or supported version cannot be preserved, rehearse a supported RabbitMQ upgrade/export/import path and verify queued messages and unacknowledged deliveries explicitly before production cutover. Keep the source container and volume untouched until restored queues are ready and both `messages_ready` and `messages_unacknowledged` are verified. Do not continue on definitions-only proof.

Take a PostgreSQL backup before the change. Close new Bot/external HTTP ingress and prevent new periodic ticks. Let the old Scheduler finish accepted `track.requests`, then stop Scheduler so it creates no new periodic work. Keep the old Gateway `price.results` consumer, Extractor consumers, and Notifier consumers running while `track.requests` drains and then while dependent `scraper.tasks`, `lookup.tasks`, `price.results`, and `notify.tasks` drain in dependency order. Check both ready and unacknowledged counts with `rabbitmqctl list_queues name durable messages_ready messages_unacknowledged consumers`. Only after all old-protocol queues are empty stop the remaining consumers and services and then the old broker. Take and verify the broker data backup while it is stopped. Copy the old anonymous volume contents to the project-scoped named volume without deleting or pruning the source. Keep the source stopped and available as rollback evidence until the new deployment is reconciled. The detailed order is also in the [README cutover checklist](../README.md#переход-на-versioned-queue-protocol).

The relevant Docker volume source can be inspected without changing resources:

```powershell
docker inspect <old-rabbitmq-container> --format '{{json .Mounts}}'
docker exec <old-rabbitmq-container> rabbitmqctl eval 'node().'
```

Do not use `docker compose down -v`, `docker volume prune`, or a hostname/nodename change as a migration method. First rehearse the exact copy or upgrade on a backup and confirm expected queues, persistent ready messages, and unacknowledged message behavior.
