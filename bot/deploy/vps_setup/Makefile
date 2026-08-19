ifndef ENV
$(error ENV is required. Usage: make <target> ENV=prod)
endif
INVENTORY = inventories/$(ENV)/hosts.yml
EXTRA_VARS = inventories/$(ENV)/env.yml

### Подготовка машины ###

init-vps: upgrade server

upgrade:
	ansible-playbook -i $(INVENTORY) upgrade.yml

server:
	ansible-playbook -i $(INVENTORY) -e @$(EXTRA_VARS) server.yml

### Выкатка ###

deploy:
	ansible-playbook -i $(INVENTORY) -e @$(EXTRA_VARS) deploy.yml

# Миграции отдельной командой и только руками: схема базы лидов не
# должна меняться побочным эффектом выкатки.
migrate:
	ansible-playbook -i $(INVENTORY) -e @$(EXTRA_VARS) deploy.yml --tags migrate

# Проверка, что бот жив и отвечает снаружи.
check:
	ansible-playbook -i $(INVENTORY) -e @$(EXTRA_VARS) deploy.yml --tags check
