# source a0b4a6f30feccf8c
# =============================================================================
# Configuración del servidor Matriline
# =============================================================================
# Este archivo tiene tres partes:
#   BÁSICO        los pocos ajustes que cambian casi todos los servidores (empieza aquí)
#   AVANZADO      todo lo demás, por sección; los valores por omisión suelen bastar
#   EXPLANATIONS  qué hace cada opción (explicaciones), en el mismo orden que AVANZADO
# Secciones: [general] idioma              [spool] directorio de trabajo
#            [network] dirección, seguridad, inscripción, bloqueos
#            [tasks] cola, reintentos, planificador, trabajos de varios núcleos
#            [orca] versión de la campaña, comprobaciones del ORCA de los clientes
#            [results] archivos devueltos  [report] informes de los clientes en marcha
#            [verify] comprobaciones de integridad, reputación, cuarentena
#            [storage] límites de disco    [alerts] correo, Telegram o un programa
#            [update] versiones nuevas de Matriline    [log] nivel del registro
# Una sección puede aparecer en más de una parte (sus ajustes simplemente se suman).
# Sintaxis: encabezados [section], "key = value", los comentarios empiezan con '#'. Los
# tamaños aceptan KB/MB/GB/KiB/MiB/GiB o "unlimited"; las duraciones 90s, 30m, 1h. Toda
# la política de la red se fija solo aquí: los clientes la reciben al conectarse y no
# pueden cambiarla. Casi todas las opciones se aplican con el servidor en marcha:
# "matriline-server config edit" comprueba el archivo y lo aplica (las opciones marcadas
# [restart] necesitan un reinicio).


# ================================ BÁSICO ======================================
[general]
# en: language of the menus, help, web page and alerts: "en", "es", "fr", "pt", "ar" (empty: this computer's)
# es: idioma de los menús, la ayuda, la página web y las alertas: "en", "es", "fr", "pt", "ar" (vacío: el de esta computadora)
# fr: langue des menus, de l'aide, de la page web et des alertes : "en", "es", "fr", "pt", "ar" (vide : celle de cet ordinateur)
# pt: idioma dos menus, da ajuda, da página web e dos alertas: "en", "es", "fr", "pt", "ar" (vazio: o deste computador)
# ar: لغة القوائم والمساعدة وصفحة الويب والتنبيهات: "en", "es", "fr", "pt", "ar" (فارغ: لغة هذا الحاسوب؛ الترجمة العربية آلية ولم تُراجَع بعد)
language =


[spool]
# directorio de trabajo (contiene input/ output/ ... state/); "." = el directorio de
# este archivo [restart]
root = .


[network]
# Puerto en el que este servidor espera a los voluntarios. Ábrelo en el firewall y, si
# estás detrás de un router, redirígelo (port forwarding) a esta computadora [restart].
# Ejemplo: 44100
listen = 44100

# Dirección que marcan los voluntarios para llegar aquí: tu IP pública (o un nombre de
# dominio) y el puerto de arriba. Se escribe en cada credencial: llénala antes de
# "keys issue". Con connection = relay puede quedar vacía.
# Ejemplos: 203.0.113.7:44100   lab.example.org:44100
advertise =

# Cómo se protege la conexión: "tls" (recomendado, todo cifrado), "auth" (sin cifrar) o
# "none" (solo para redes de prueba)
security = tls

# Cómo se encuentran: "direct" (los voluntarios llegan a este servidor por el puerto de
# arriba) o "relay" (si no puedes abrir ningún puerto: ambos se conectan a un relé).
# Ejemplo: connection = "relay"
connection = direct

# dirección:puerto del relé, solo con connection = relay. Ejemplo: relay.example.org:443
relay_address =

# Quién puede unirse: "issued" (tú creas un archivo de credencial por voluntario;
# recomendado), "register" (el voluntario pide unirse con un token y tú lo apruebas) u
# "open" (PELIGRO: cualquiera que tenga la dirección)
enrollment = issued


[tasks]
# "order" (orden de la cola) o "learned" (trabajos largos a los clientes rápidos)
scheduler = order

# Tiempo máximo que un cálculo puede correr en una computadora. Al llegar, el trabajo se
# detiene y pasa a errors/ con sus orbitales hasta ese momento, y 'retry' continúa desde
# ellos. 0 = sin límite. Ejemplos: 12h   3d
max_job_time = 1d


[orca]
# versión de ORCA de esta campaña: todos los clientes deben usarla
version = 6.1.1

# "fingerprint" (el ORCA de los clientes debe coincidir con las referencias de abajo) o
# "version" (confiar en ellos)
check = fingerprint

# un cliente con otra versión de ORCA: "refuse", "separate" o "errors"
other_versions = refuse

# instalación(es) de ORCA en esta computadora, separadas por comas (init la rellena)
reference_paths = /opt/orca-6.1.1

# "builtin" = las compilaciones oficiales de esta versión (Linux, Windows, macOS); más
# archivos JSON de "matriline-client fingerprint"
accepted_fingerprints = builtin


[verify]
# comprobaciones de integridad de los resultados (las opciones están en AVANZADO
# [verify])
enabled = true


[storage]
# espacio en disco que puede usar el proyecto (unlimited, o p. ej. 500G)
limit = unlimited


[alerts]
# Las alertas te llegan por correo, por Telegram o por ambos. Paso a paso para cada uno:
# al final de este archivo, en [alerts]. Luego pruébalo: matriline-server test-alert
# --- por correo
email_enabled = false

# el servidor de correo y su puerto, p. ej. smtp.gmail.com:587
smtp_server =

# la cuenta que envía (aquella en la que creaste la contraseña de aplicación)
smtp_user =

# el archivo con la contraseña de aplicación de esa cuenta (ruta completa; nunca la
# contraseña aquí)
smtp_password_file =

# el remitente que se muestra (normalmente la misma dirección)
from =

# quién recibe las alertas (separados por comas)
to =

# --- por Telegram (primero envía /start a tu bot desde tu Telegram)
telegram_enabled = false

# el archivo con el token del bot (ruta completa; nunca el token aquí)
telegram_token_file =

# tu id de chat (un número; te lo dice @userinfobot)
telegram_chat =

# --- o un programa propio, que se ejecuta con cada alerta (evento, mensaje); vacío = ninguno
command =


[update]
# versiones nuevas de Matriline: "off", "alert" (avísame; 'update apply' la instala) o "auto".
# EXPERIMENTAL: no recomendado para un proyecto en curso (ver EXPLANATIONS)
mode = off


[log]
# "debug", "info", "warn" o "error"
level = info


# =============================== AVANZADO ====================================
# Los valores por omisión sirven a casi todos los proyectos; cada opción se explica al
# final del archivo.


[spool]
keep_empty_dirs = input


[network]
accept_nosecurity_clients = false

credential_valid = 48h

blocked_ips =

max_sessions = 256

ban_garbage_after = 10

ban_garbage_window = 1h

ban_garbage_duration = 168h

ban_auth_after = 20

ban_auth_window = 24h

ban_auth_duration = 24h

ban_trust_known = true


[tasks]
reuse_checkpoints = true

heartbeat_seconds = 60

task_timeout = 1h

scan_interval = 5s

settle_time = 2s

order = alphanumeric

input_extensions = .inp

duplicate_when_idle = true

max_replicas = 2

orca_error_hosts = 2

max_attempts = 6

error_alert_after = 3

error_pause_after = 10

error_window = 24h

learned_min_samples = 30

parallel = downgrade

max_procs = 8


[orca]
accepted_tree_hashes =


[results]
include = *

exclude = *.tmp, *.tmp.*

max_bytes = 2G

compress_level = 6


[report]
fields = *

percentiles = 50, 90, 99

sample_seconds = 5


[verify]
fingerprints = true

output_consistency = true

timing_plausibility = true

scf_recheck_fraction = 0.05

gradient_check_fraction = 0.05

hessian_probe_fraction = 0.05

canary_rate = 0

replication_rate = 0

probation_results = 5

probation_replication = 0.2

probation_multiplier = 3

on_failure = weird

energy_tolerance = 1e-5

check_max_scf_iterations = 10

hessian_tolerance = 0.005

reputation = true

trusted_after = 50

trusted_multiplier = 0.5

recent_failure_window = 720h

recent_failure_multiplier = 2

second_opinion = true

second_opinion_unavailable = weird

retry_weird = true

rescue_weird = true

quarantine_after = 3

quarantine_window = 24h

quarantine_recheck = 10

quarantine_escalate_fraction = 0.25

quarantine_auto_release = true


[storage]
warn_percent = 90

min_free = auto


[alerts]
smtp_password_env = MATRILINE_SMTP_PASSWORD

# cada alerta: off, now (al momento) o summary (en el resumen programado)
# el disco está casi lleno, o se rechazan resultados por falta de espacio
storage = now

# un resultado no pasó una comprobación (fue a weird/)
verify_failed = now

# un cliente entró en cuarentena (se comprueba cada resultado suyo)
quarantine = now

# un cliente dejó de informar los cálculos que estaba corriendo
client_lost = now

# una tarea se perdió o se duplicó ('check')
tasks = now

# todas las entradas están terminadas: nada en cola, corriendo ni en comprobación
campaign_done = now

# una versión nueva de Matriline, o un problema al instalarla
update = now

# una tarea fue a errors/ (ORCA falló), o un cliente se pausó tras varios errores
errors = summary

# un cliente con una versión de ORCA distinta de la de esta campaña
orca_version = summary

# un cliente nuevo se inscribió con su credencial
enrolled = summary

# se bloqueó una dirección (escáneres de puertos, inicios de sesión fallidos)
ban = summary

# la misma alerta se envía "now" como mucho una vez por este intervalo (las demás van al
# resumen)
now_limit = 1h

# cuándo se envía el resumen: off, hourly, every 6h, daily 08:00, daily 08:00 20:00,
# weekdays 08:00, weekends 10:00, mon,thu 09:00, mon-fri 18:30 ...
summary = daily 08:00

# enviar el resumen también cuando no pasó nada ("todo bien", con el estado del
# proyecto)
summary_when_quiet = false


[hooks]
result =

done =


[web]
palette = A

mol_rotation = 20s

mol_background = auto

white =

black =

gray =

dark =

light =


[update]
check_interval = 720h

url = https://github.com/agaloya/matriline/releases

client_wait = 24h


# ============================= EXPLANATIONS ==================================
#
# ---------------- [general] ----------------
# >> language
# en: The language of what people read: console menus, 'help', the web page, the alerts.
#   Empty: this computer's language (LANG, or the Windows display language); English when
#   there is no translation. Logs and error messages stay in English.
# es: El idioma de lo que se lee: menús de la consola, 'help', la página web, las alertas.
#   Vacío: el idioma de esta computadora; inglés si no hay traducción. El registro y los
#   mensajes de error quedan en inglés.
# fr: La langue de ce qu'on lit : menus de la console, 'help', la page web, les alertes.
#   Vide : la langue de cet ordinateur ; l'anglais s'il n'y a pas de traduction. Le journal
#   et les messages d'erreur restent en anglais.
# pt: O idioma do que se lê: menus do console, 'help', a página web, os alertas. Vazio: o
#   idioma deste computador; inglês se não houver tradução. O log e as mensagens de erro
#   ficam em inglês.
#
# ---------------- [spool] ----------------
# >> root
# Directorio de trabajo. Contiene input/ output/ completed/ cancelled/ paused/ weird/
# errors/ más el directorio interno state/. Las rutas relativas son relativas a este
# archivo.
#
# >> keep_empty_dirs
# Directorios del spool cuyos subdirectorios vacíos se conservan (separados por comas).
# En los demás, un subdirectorio que queda vacío cuando su última tarea sigue su camino
# se borra. input = tu propia estructura para los lotes nuevos se conserva; p. ej.
# "input, output" conserva también la de output/.
# ---------------- [network] ----------------
# >> accept_nosecurity_clients
# accept_nosecurity_clients: dar tareas a clientes compilados con "-tags nosecurity"
# (sin entorno aislado, o sandbox, ni auditoría de procesos; pensado para máquinas
# propias de confianza).
# Desactivado: se les avisa y no reciben tareas.
# >> listen
# Direcciones TCP en las que escuchar [restart]. Puerto por omisión 44100 (no asignado
# por la IANA). Un puerto solo escucha en todas las direcciones de esta computadora. Para
# escuchar en una sola (una computadora con varias redes), escribe dirección:puerto, p.
# ej. 192.168.1.10:44100. Varias entradas van separadas por comas, p. ej. 44100, 443. En las redes más restrictivas solo se permite TCP/443 de salida; añadir
# "443" (requiere privilegios en casi todos los sistemas) permite que esos clientes
# lleguen a un servidor alcanzable directamente.
# >> advertise
# Dirección (host:port) que se escribe en los archivos de credencial entregados a los
# clientes. Déjala vacía para que se pregunte al emitir llaves.
# >> security
# Modo de seguridad para TODAS las conexiones:
#   tls  - (por omisión, recomendado) TLS 1.3, ambos lados autenticados, todo cifrado.
#   auth - cada mensaje autenticado y protegido contra modificación y repetición, pero
#          SIN cifrar: cualquiera en el camino de red puede leer entradas y resultados.
#   none - ninguna protección después de la comprobación inicial de identidad. PELIGRO:
#          un atacante en el camino puede leer y alterar todo. Solo para redes de prueba
#          aisladas.
# >> connection, relay_address
# Cómo llegan los clientes a este servidor:
#   direct - los clientes se conectan a esta máquina. El servidor necesita UN puerto TCP
#            alcanzable (reenvíalo en tu router si el servidor está detrás de NAT). Los
#            clientes nunca necesitan reenvío de puertos.
#   relay  - el servidor y los clientes se conectan HACIA FUERA a un relé con IP pública
#            (matriline-relay). Nadie necesita reenvío de puertos. El relé solo reenvía
#            el flujo protegido de extremo a extremo y no puede leerlo (no puede leer
#            nada útil con security = tls; con auth/none SÍ puede leer el texto en
#            claro).
# >> enrollment
# Quién puede unirse:
#   issued   - (por omisión) el administrador crea un archivo de credencial por usuario
#              con "matriline-server keys issue <name>" y se lo entrega. El archivo es
#              un boleto de un solo uso: en su primera conexión el cliente crea su
#              propia llave de equipo (nunca sale de esa computadora) y el archivo deja
#              de funcionar, así que una copia robada después no da acceso. Revocable en
#              cualquier momento.
#   register - los usuarios generan su propia llave y se unen con un token de un solo
#              uso ("matriline-server keys token"); el administrador los aprueba
#              ("matriline-server clients approve <id>").
#   open     - PELIGRO: cualquier máquina que conozca la dirección y la llave del
#              servidor puede unirse y recibirá tus archivos de entrada.
# >> credential_valid
# Cuánto tiempo se puede usar una credencial de "keys issue" para su única inscripción.
# >> blocked_ips
# Direcciones IP rechazadas antes de cualquier negociación (handshake), separadas por
# comas, para siempre.
# >> max_sessions
# Máximo de clientes conectados a la vez; las conexiones siguientes se rechazan hasta
# que termine una (un límite contra una avalancha de conexiones; súbelo para una campaña
# con más voluntarios).
# >> ban_garbage_after, ban_garbage_window, ban_garbage_duration
# Bloqueos automáticos (lístalos con "matriline-server bans", levanta uno con
# "matriline-server bans lift <address>"). 0 en una opción *_after desactiva esa regla.
# Conexiones que nunca hablan el protocolo de Matriline (escáneres de puertos, otros
# protocolos, basura, conexiones vacías): esta cantidad dentro de la ventana bloquea la
# dirección.
# >> ban_auth_after, ban_auth_window, ban_auth_duration
# Conexiones que hablan el protocolo pero no son admitidas (llave desconocida,
# equivocada o revocada, token inválido). A los clientes honestos les puede pasar de vez
# en cuando (una credencial vieja, un enlace que se cae a mitad de la negociación),
# así que el margen es mayor y el bloqueo es temporal. Las reconexiones de clientes
# admitidos nunca cuentan.
# >> ban_trust_known
# Nunca bloquear automáticamente una dirección desde la que se conectó un cliente
# registrado en las últimas 24 h: en un NAT compartido (oficina, hotel, operador
# móvil) un atacante podría, si no, dejar fuera a un voluntario legítimo. Sus intentos
# fallidos se siguen rechazando y generando alertas.
#
# ---------------- [tasks] ----------------
# >> reuse_checkpoints
# Privacidad de las entradas: los clientes ven la molécula y el método de lo que
# calculan (deben verlos, para correr ORCA); los nombres reales de los archivos, los
# subdirectorios y quién los envió nunca se mandan. Sin implementar, una sugerencia para
# quien amplíe este código: una opción para mantener las entradas sensibles (p. ej. un
# subdirectorio como input/private/) solo para clientes elegidos y, en el hardware que
# lo ofrezca (AMD SEV-SNP, Intel TDX), máquinas virtuales confidenciales atestiguadas
# cuya memoria no puede leer el dueño de la computadora (docs/DECISIONS.md D44).
# reuse_checkpoints: cuando un equipo falla una tarea (falla de la máquina o tiempo
#   agotado, no un error de ORCA), conservar los orbitales (.gbw) a los que llegó y
#   dárselos al siguiente equipo: ORCA empieza desde ellos por sí solo (AutoStart lee
#   <input name>.gbw), así que un SCF largo no empieza de cero y la entrada no cambia.
#   Solo de equipos de confianza (activos, ya fuera del periodo de prueba, sin
#   comprobaciones fallidas): un .gbw falsificado no puede cambiar el método pero podría
#   llevar el SCF a otra solución autoconsistente (p. ej. un estado de simetría rota de
#   un radical).
# >> heartbeat_seconds
# Los clientes envían un latido con esta frecuencia (segundos).
# >> task_timeout
# Una tarea en curso de la que su cliente no informa durante este tiempo se da a otro
# equipo (el primer resultado válido sigue ganando si el cliente original vuelve
# después).
# >> scan_interval, settle_time
# Cada cuánto se vuelve a revisar input/ en busca de archivos nuevos, y cuánto tiempo
# debe quedarse sin cambios un archivo nuevo antes de ponerlo en cola (protege contra
# archivos copiados a medias).
# >> order, input_extensions
# Orden en que se reparten las entradas: alphanumeric (orden natural de subdirectorios y
# archivos, así que se respeta 1list, 2list, ... 10list) o random. Las prioridades
# explícitas fijadas con "matriline-server priority" siempre ganan.
# >> duplicate_when_idle, max_replicas
# Cuando ya no quedan tareas en cola, las tareas en curso pueden duplicarse en equipos
# libres para que una máquina muy lenta no retrase el final de una campaña. Gana el
# primer resultado verificado.
# >> orca_error_hosts
# Una tarea va a errors/ cuando el propio ORCA falla en esta cantidad de equipos
# DISTINTOS. Los problemas de la máquina (corte de luz, falta de memoria, desconexión)
# no cuentan.
# >> max_attempts
# Límite absoluto de intentos por tarea (por cualquier causa) antes de errors/.
# >> max_job_time
# Tiempo máximo de un trabajo en una computadora (trabajos enviados desde entonces). El
# voluntario detiene el trabajo al llegar y devuelve lo que tiene; la tarea pasa a errors/
# con sus orbitales (.gbw) y una nota (verdict) que dice cómo continuar: sube el límite,
# luego 'matriline-server retry errors/<tarea>', y la siguiente computadora parte de esos
# orbitales. El limits.max_task_time propio de un voluntario, si es menor, solo pasa la
# tarea a otra computadora.
# >> error_alert_after, error_pause_after, error_window
# Errores de un cliente (errores de ORCA o fallas de la máquina, no trampas):
# error_alert_after: alerta al administrador tras esta cantidad de errores seguidos (0 =
# desactivado). error_pause_after: pausa el cliente (sin tareas nuevas hasta "clients
# release <name>") tras
#   esta cantidad de errores dentro de error_window, p. ej. una computadora que no puede
#   correr cierto tipo de trabajo.
# >> scheduler, learned_min_samples
# scheduler: qué tarea de la cola recibe un cliente, dentro del nivel de prioridad más
# alto:
#   order   - (por omisión) el orden de arriba.
#   learned - el servidor aprende de sus resultados aceptados cuánto tarda cada tipo de
#             trabajo (método, base, tamaño, Opt/Freq, capa abierta) y qué tan rápido es
#             cada cliente, y da los trabajos más largos previstos a los clientes más
#             rápidos y los más cortos a los más lentos (campañas más cortas con
#             hardware variado). Antes necesita learned_min_samples resultados; hasta
#             entonces, y para clientes o entradas que no puede evaluar, vuelve a
#             "order". Ver "matriline-server model".
# >> parallel, max_procs
# parallel: entradas que piden varios núcleos ("%pal nprocs N", "! PALN"):
#   downgrade - (por omisión) corren en un núcleo, como cualquier otro trabajo (varios
#               trabajos independientes aprovechan mejor los núcleos: medido, 2 núcleos
#               hacen un trabajo ~1.25x más rápido).
#   honor     - corren con N procesos MPI (como mucho max_procs) en un cliente que
#               acepta trabajos de ese tamaño (su resources.max_cores_per_job) y tiene N
#               núcleos libres; mientras ninguno los tenga, la cola reparte las tareas
#               siguientes.
#
# ---------------- [orca] ----------------
# >> version
# Versión de ORCA de esta campaña: todos los clientes deben usarla. Fíjala antes de las
# primeras entradas; si cambia después, el servidor avisa (registro, alerta, bitácora),
# ya que entonces output/ mezcla resultados de dos versiones.
# >> check
# check: cómo se comprueba el ORCA de un cliente.
#   fingerprint - (por omisión) cada programa que corrió un resultado debe pertenecer a
#                 una de las compilaciones de referencia de abajo: pon aquí el ORCA
#                 de cada sistema operativo y arquitectura que usen tus clientes
#                 (Linux x86-64, Linux arm64, Windows, macOS), desempaquetado; el
#                 servidor solo calcula el hash de sus archivos, no ejecuta los de
#                 otras plataformas.
#   version     - confiar en las instalaciones de los clientes: solo la versión y la
#                 compilación que ORCA escribe en cada salida (y el manifiesto) deben
#                 ser las de esta campaña.
# >> other_versions
# other_versions: qué hacer con un cliente que no tiene la versión de esta campaña.
#   refuse   - (por omisión) no recibe tareas; se le dice qué versión instalar, y el
#              administrador recibe una alerta (orca_version).
#   separate - calcula con su propia versión; sus resultados cuentan como terminados
#              pero se guardan aparte, en other-versions/<version>/output|weird|errors,
#              nunca en output/. NO se comprueban de forma cruzada (una comprobación
#              con otra versión compararía dos programas), así que un cliente podría
#              declarar una versión vieja para saltarse las comprobaciones: úsalo
#              solo con clientes de confianza.
#   errors   - calcula con su propia versión, sus resultados van a
#              errors/other-versions/<version>/ como referencia, y la entrada sigue en
#              cola para un cliente con la versión de esta campaña.
# >> reference_paths
# Instalación(es) de referencia en esta máquina que se usan para calcular las huellas
# esperadas. Se permiten varias rutas, p. ej. la compilación x86-64 y la arm64 de la
# misma versión (desempaquetadas aquí; a una compilación de otra arquitectura se le
# calcula la huella pero no se puede ejecutar).
# >> accepted_fingerprints
# Instalaciones aceptadas por su huella completa, separadas por comas. "builtin" = las
# compilaciones oficiales de ORCA de orca.version que conoce este programa (6.1.1: Linux
# x86-64 y arm64, Windows x86-64, macOS x86-64 y arm64): solo hashes, así que este
# servidor no necesita ORCA en absoluto (reference_paths puede quedar vacío; los
# clientes calculan cada comprobación). Otras compilaciones: archivos JSON hechos en un
# cliente con "matriline-client fingerprint". Mejor que accepted_tree_hashes: así el
# servidor puede comprobar cada programa que corrió un resultado, también para
# compilaciones que no puede ejecutar aquí o que no imprimen un hash de GIT (la
# compilación oficial arm64 de ORCA 6.1.1).
# >> accepted_tree_hashes
# Huellas de instalación aceptadas adicionales (hashes de árbol), separadas por comas,
# p. ej. cuando la máquina del servidor no tiene ORCA instalado.
#
# ---------------- [results] ----------------
# >> include, exclude
# Qué archivos producidos por ORCA se devuelven (patrones glob). Por omisión: todo
# excepto los archivos temporales de ORCA (*.tmp y *.tmp.N: integrales e intermedios, a
# menudo de varios GB, inútiles después; un trabajo DLPNO fallido dejó 6.7 GB de *.tmp.0
# en el laboratorio). Los clientes borran lo que no se devuelve. Para recibirlos igual,
# deja exclude vacío. Ejemplo para ahorrar ancho de banda:
# include = *.out, *.property.txt, *.xyz
# >> max_bytes
# Rechaza resultados más grandes que esto (protege tu disco de clientes maliciosos).
#   Suficiente para resultados normales (.gbw, .hess, trayectorias); los temporales de
#   ORCA se excluyen arriba.
# >> compress_level
# Compresión de los archivos transferidos: 0 = ninguna ... 9 = lo más pequeño (más CPU).
# 6 es un equilibrio.
#
# ---------------- [report] ----------------
# >> fields
# Campos del informe de metadatos de cada ejecución (matriline.report) que se recogen de
# los clientes. "*" = todos. Los prefijos que terminan en '*' eligen grupos, p. ej.:
# time.*, cpu.*, mem.*, net.*
# >> percentiles
# Percentiles que se informan para las series muestreadas (temperaturas, memoria, RTT,
# ...).
# >> sample_seconds
# Periodo de muestreo de la telemetría del cliente mientras corre un trabajo (segundos).
#
# ---------------- [verify] ----------------
# >> enabled
# Sin implementar, una sugerencia para quien amplíe este código: atestación de hardware.
# En CPU con computación confidencial (AMD SEV-SNP, Intel TDX) un cliente podría correr
# ORCA en una máquina virtual confidencial atestiguada cuya memoria el dueño de la
# computadora no puede leer ni cambiar, y enviar un informe firmado por el hardware de
# que corrió exactamente esa imagen; los resultados de esos clientes podrían entonces
# necesitar menos comprobaciones de las de abajo, y sus entradas quedan privadas
# (docs/DECISIONS.md D44). Verificación de integridad. Cada resultado siempre lo firma
# su cliente y se registra en la bitácora encadenada por hashes del servidor (eso
# identifica quién lo produjo). Los valores por omisión activan todas las capas que no
# cuestan cálculo y comprueban una muestra del 5 % con las comprobaciones activas
# baratas: en las rondas de ataque del laboratorio esto atrapó todas las falsificaciones
# con mucho menos del 1 % de cálculo extra (docs/SECURITY_TESTS.md). Un cliente en
# cuarentena sigue calculando, pero se comprueba CADA resultado que devuelve. Cada una
# de las demás capas cuesta cálculo extra; las fracciones van de 0 a 1 (1 = todos los
# resultados).
#
# enabled: interruptor general. false = ninguna capa de integridad, digan lo que digan
#   las opciones de abajo (para computadoras de total confianza); los resultados siguen
#   firmados. Los resultados se siguen revisando en busca de errores de ORCA y de
#   optimizaciones que no convergieron. true = aplicar las opciones de abajo.
# >> fingerprints
#
# fingerprints: el cliente calcula el hash de toda la instalación de ORCA y de cada
#   programa que realmente corrió. Detecta versiones equivocadas e instalaciones
#   dañadas. Un cliente modificado a propósito puede mentir, así que esto atrapa sobre
#   todo errores honestos.
# >> output_consistency
# output_consistency: analiza la salida de ORCA: versión y hash de GIT, terminación
#   normal, entrada repetida idéntica a la de la tarea, módulos esperados. Gratis,
#   recomendado.
# >> timing_plausibility
# timing_plausibility: el tiempo de ejecución que declara el cliente y que imprime ORCA
#   no puede superar el tiempo desde que se asignó la tarea (+2 min). Señala salidas
#   copiadas de otro lado. Gratis.
# >> scf_recheck_fraction
# scf_recheck_fraction: recalcula la energía a partir de los orbitales devueltos (.gbw)
#   con UNA iteración SCF en otro equipo: un resultado convergido auténtico reproduce la
#   energía y converge de inmediato. Cuesta ~5% de un SCF. Requiere que se devuelvan los
#   .gbw.
# >> gradient_check_fraction
# gradient_check_fraction: en las optimizaciones, calcula el gradiente en la geometría
#   final en otro equipo; debe ser ~0. Cuesta ~un gradiente.
# >> hessian_probe_fraction
# hessian_probe_fraction: en las frecuencias, compara H*v de la hessiana devuelta con
#   una diferencia finita de dos gradientes a lo largo de una dirección aleatoria
#   secreta v.
# >> canary_rate
# canary_rate: por cada tarea real asignada, probabilidad de enviar también a ese
#   cliente una tarea cuya respuesta se conoce (un resultado pasado verificado, rotado,
#   trasladado y reordenado al azar para que no se pueda reconocer). Costo: canary_rate
#   cálculos de punto único (single point) extra por tarea.
# >> replication_rate
# replication_rate: fracción de tareas que también se corren en un segundo equipo
#   distinto y se comparan.
# >> probation_results
# Los clientes nuevos reciben "probation_multiplier" veces más comprobaciones en sus
# primeros N resultados.
# >> probation_replication, probation_multiplier
# probation_replication: durante el periodo de prueba, recalcular también esta fracción
#   de los resultados del cliente en otro equipo (la comprobación genérica para tipos de
#   trabajo que las comprobaciones baratas no pueden reconstruir).
# >> on_failure
# Qué pasa cuando falla una comprobación: weird (el resultado va a weird/, el cliente
# sigue trabajando) o quarantine (además deja de enviar tareas a ese cliente y vuelve a
# comprobar sus resultados pasados).
# >> energy_tolerance
# Diferencia máxima de energía (Hartree) aceptada al comparar dos cálculos (réplicas).
# Los equipos honestos difieren hasta ~2e-6 Eh (UKS + RIJCOSX, visto en el laboratorio);
# 1e-5 Eh son 0.006 kcal/mol, muy por debajo de cualquier falsificación químicamente
# útil (se atrapó una de 2e-4 Eh).
# >> check_max_scf_iterations
# check_max_scf_iterations: tope de iteraciones SCF de las comprobaciones scf/gradiente,
#   que parten de los orbitales devueltos (los auténticos convergen en pocos
#   ciclos; se vieron 4 para un fenol). Una comprobación que no converge dentro del tope
#   es NO CONCLUYENTE, no una prueba de trampa: el resultado va a errors/ con un
#   veredicto que nombra esta opción, para que puedas subirla y usar "retry". Más alto =
#   menos comprobaciones no concluyentes, un poco más de cálculo por comprobación.
# >> hessian_tolerance
# hessian_tolerance: mayor discrepancia relativa ||H v - diferencia finita|| aceptada
#   por la sonda de la hessiana. Corridas honestas medidas en el laboratorio: HF
#   0.00004, PBE 0.0006, B3LYP/RIJCOSX 0.0018 (mallas numéricas). Una hessiana inflada
#   un x % da cerca de x/100, así que 0.005 atrapa frecuencias desplazadas más de ~0.25
#   % (0.02 dejó pasar una falsificación del 2 %).
# >> reputation, trusted_after, trusted_multiplier, recent_failure_window, recent_failure_multiplier, second_opinion
# second_opinion: cuando todas las subcomprobaciones de un resultado corrieron en UN
#   solo equipo, repetir la más barata (SCF, gradiente o réplica) en otro equipo
#   independiente antes de aceptarlo, y tratar a los verificadores que discrepan como
#   una disputa (weird/, nadie es penalizado). Defiende contra dos clientes coludidos
#   que se aprueban mutuamente resultados falsificados (prueba de laboratorio: 6 de 12
#   falsificaciones pasaron sin esto). Costo: una comprobación extra barata para las
#   comprobaciones de un solo equipo.
# Reputación (después del periodo de prueba): un cliente con trusted_after
#   comprobaciones superadas y ninguna fallida dentro de recent_failure_window tiene sus
#   fracciones de comprobación multiplicadas por trusted_multiplier (nunca cero: el
#   muestreo sigue siendo impredecible); un cliente con una comprobación fallida dentro
#   de esa ventana recibe en cambio recent_failure_multiplier.
# >> second_opinion_unavailable
# second_opinion_unavailable: qué hacer con un resultado comprobado en un solo equipo
#   cuando ningún otro equipo independiente puede dar la segunda opinión (ninguno
#   conectado, o caducó): weird (por omisión, elegido por el usuario) lo manda a weird/
#   para un desempate o un recálculo; accept lo conserva (sí pasó una comprobación). En
#   un servidor con menos de tres clientes nunca hay un tercer equipo: ahí, "weird"
#   manda a weird/ casi todos los resultados comprobados (se registra un aviso), así que
#   las instalaciones pequeñas pueden preferir accept.
# >> retry_weird
# retry_weird: cuando un resultado acaba en weird/, volver a poner su entrada al FINAL
#   de la cola (una vez por tarea), excluyendo al cliente que lo produjo; el resultado
#   sospechoso se queda en weird/ para revisión. false = dejar la tarea sin hacer (una
#   alerta lo dice).
# >> rescue_weird
# rescue_weird: un resultado en weird/ SIN prueba de trampa (los verificadores
#   discrepan, o una comprobación fallida que nadie pudo confirmar) recibe primero una
#   comprobación de desempate barata (recomprobación SCF, o una réplica sin orbitales)
#   en un equipo que no participó; si la pasa, el resultado vuelve a output/ y el
#   trabajo no se recalcula. Si no, se aplica retry_weird. Las falsificaciones
#   confirmadas (dos equipos contra el productor) nunca se rescatan.
# >> same_host
# Las comprobaciones pueden correr en la computadora que produjo el resultado. Solo para
# una única computadora (Nacomline): ahí pueden atrapar una máquina defectuosa (memoria
# dañada, sobrecalentamiento), nunca a un tramposo. Por omisión false: un resultado
# siempre se comprueba en otra computadora.
# >> quarantine_after, quarantine_window, quarantine_recheck
# quarantine_after: un cliente con esta cantidad de verificaciones fallidas dentro de
#   quarantine_window entra en cuarentena automáticamente: sigue calculando, se
#   comprueba cada resultado que devuelve y se alerta al administrador ("clients drain"
#   detiene sus tareas por completo). Las fallas aisladas se toleran. 0 = nunca
#   automáticamente.
# quarantine_recheck: en cualquier cuarentena (automática o del administrador), volver a
#   verificar esta cantidad de los resultados aceptados más recientes del cliente en
#   equipos independientes (0 = ninguno).
# >> quarantine_escalate_fraction
# quarantine_escalate_fraction: si un resultado recomprobado está mal, recomprobar esta
#   fracción de TODOS los resultados aceptados del cliente (una muestra aleatoria; 1 =
#   todos, 0 = sin escalar).
# >> quarantine_auto_release
# quarantine_auto_release: liberar al cliente automáticamente cuando todas sus
#   recomprobaciones son correctas (la detección puede equivocarse; nadie tiene que
#   desbloquear clientes a mano). Los resultados malos se quedan en weird/ y sus tareas
#   se reintentan de todos modos.
#
# ---------------- [storage] ----------------
# >> limit, warn_percent
# Espacio máximo en disco para el spool (output/ etc.). unlimited por omisión. Los
# resultados nuevos se rechazan (los clientes los conservan y reintentan después) al
# alcanzar el límite.
# >> min_free
# Mantener al menos esto libre en el disco que contiene el proyecto: un resultado que
# dejaría menos (contando las subidas en curso) se rechaza y el cliente reintenta
# después. Impide que cualquiera con una llave, o un trabajo desbocado, llene el disco.
# auto = 5 % del disco, como mucho 5G (un 5G fijo rechazó todos los resultados en un
# disco pequeño de 8 GB en el laboratorio); 0 = desactivado.
#
# ---------------- [alerts] ----------------
# >> email_enabled, smtp_server, smtp_user, smtp_password_file, from, to, smtp_password_env
# Alertas por correo, enviadas por SMTP (sirve cualquier cuenta de correo; no hace falta
# un proyecto de Google Cloud ni OAuth). Nunca escribas la contraseña en este archivo:
# va en smtp_password_file (un archivo con solo la contraseña, que solo tú puedas leer).
# smtp_password_env nombra en cambio una variable de entorno (un servicio no ve las
# variables de tu terminal: usa el archivo).
#
# Paso a paso (Gmail; otros proveedores son parecidos, ver su ayuda sobre "SMTP"):
#   1. Usa una cuenta para las alertas (una nueva es buena idea) y activa su
#      verificación en 2 pasos: https://myaccount.google.com/security
#   2. Ve directo a https://myaccount.google.com/apppasswords (los menús la esconden),
#      escribe "Matriline" y pulsa Crear. Google muestra 16 letras una sola vez.
#   3. Guárdalas en un archivo que solo tú puedas leer, sin que aparezcan en pantalla:
#        Linux/macOS: umask 077; read -rs -p "App password: " p; printf '%s' "$p" > ~/.keys/matriline-mail; unset p
#        Windows (PowerShell): $p = Read-Host -AsSecureString "App password";
#          [Net.NetworkCredential]::new('', $p).Password | Set-Content -NoNewline $HOME\matriline-mail
#   4. En [alerts] arriba: email_enabled = true, smtp_server = smtp.gmail.com:587,
#      smtp_user = la dirección de la cuenta donde creaste la contraseña de aplicación
#      (la dirección de otra cuenta se rechaza: "Username and Password not accepted"),
#      smtp_password_file = la ruta completa del archivo (p. ej.
#      /home/<you>/.keys/matriline-mail), from = esa dirección, to = quién recibe las
#      alertas.
#   5. matriline-server config reload, luego matriline-server test-alert: debe llegar un
#      correo de prueba (busca en la carpeta de spam la primera vez).
# Si Google dice que la opción "no está disponible para tu cuenta": la verificación en 2
# pasos está desactivada, o la cuenta pertenece a una institución que bloquea las
# contraseñas de aplicación (usa otra cuenta, o Telegram). Otros proveedores: Yahoo
# smtp.mail.yahoo.com:587 (contraseña de aplicación), un servidor de empresa o
# institución: pregunta a su área de TI. La conexión va cifrada (STARTTLS).
# >> telegram_enabled, telegram_token_file, telegram_chat
# Alertas por Telegram, desde un bot tuyo (la forma más fácil). Paso a paso:
#   1. En Telegram, abre @BotFather, envía /newbot, dale al bot un nombre (p. ej. "Mis
#      alertas de Matriline") y un nombre de usuario que termine en "bot". BotFather
#      responde con un token.
#   2. Guarda el token en un archivo que solo tú puedas leer (como en el paso 3 del
#      correo, arriba), p. ej.
#        umask 077; read -rs -p "Bot token: " t; printf '%s' "$t" > ~/.keys/matriline-telegram; unset t
#   3. Abre tu bot nuevo en Telegram (busca su nombre de usuario) y envíale /start. Sin
#      esto, un bot no puede escribirte.
#   4. Tu id de chat: abre @userinfobot en Telegram y envía /start; te responde con tu
#      Id (un número).
#   5. En [alerts] arriba: telegram_enabled = true, telegram_token_file = la ruta
#      completa del archivo del token, telegram_chat = tu Id.
#   6. matriline-server config reload, luego matriline-server test-alert: el bot te
#      escribe. Para un grupo: añade el bot al grupo y usa el id del grupo (negativo).
# >> command
# test-alert (o 'alerts test') envía al momento una alerta de prueba por cada canal
# activo, y dice cuáles funcionaron. Programa de alertas: se ejecuta con cada alerta
# (mismos eventos y límite) con dos argumentos, el evento y el mensaje, y sin shell; p.
# ej. un script que la envía por Telegram o ntfy. (Telegram viene incluido:
# telegram_enabled.) Una ruta absoluta; vacío = desactivado. Puede correr 30 s como
# mucho.
# >> storage, verify_failed, quarantine, client_lost, tasks, campaign_done, update, errors, orca_version, enrolled, ban
# Cada alerta puede ser off, now o summary. now: se envía por correo (y se le da al
# programa de alertas) al momento; la misma alerta como mucho una vez por now_limit, las
# demás van al resumen. summary: se guardan y se envían juntas según el horario del
# resumen. off: solo en state/events.log. Los archivos escritos antes de estas opciones
# ("events = ..." y "digest = ...") conservan su significado.
# >> now_limit
# La misma alerta se envía "now" como mucho una vez por este intervalo (por omisión 1h).
# >> summary
# Cuándo se envía el resumen, en palabras simples: off; hourly; every 30m, every 6h
# (desde medianoche); daily 08:00 (varias veces: daily 08:00 20:00); weekdays 08:00;
# weekends 10:00; mon,thu 09:00; mon-fri 18:30; weekly mon 09:00. Con el reloj de esta
# computadora. Las alertas guardadas cuando el servidor se detiene se envían en ese
# momento.
# >> summary_when_quiet
# true: el resumen se envía aunque no haya habido alertas, con el estado del proyecto
# (un "todo bien" diario); false: solo cuando hay algo que informar.
#
# ---------------- [hooks] ----------------
# Programas que se ejecutan cuando algo pasa, para mover, copiar, subir o encadenar
# resultados (una ruta absoluta; vacío = ninguno). Corren de uno en uno, sin shell, como
# mucho 10 minutos cada uno; una falla se registra y se anota en state/events.log.
# >> result
# Se ejecuta con cada resultado que llega a output/, weird/ o errors/, o que se mueve
# ahí, con los argumentos: result <task> <output|weird|errors> <result folder> (también
# en el entorno: MATRILINE_EVENT, MATRILINE_TASK, MATRILINE_VERDICT,
# MATRILINE_RESULT_DIR).
# Ejemplo: un script que copia los resultados de output/ a una carpeta en la nube, o que
# escribe la siguiente entrada de una cadena (p. ej. un trabajo de frecuencias a partir
# de una geometría optimizada) en input/.
# >> done
# Se ejecuta cuando la cola se vacía (todas las entradas terminadas, nada corriendo):
# done <summary>.
#
# ---------------- [web] ----------------
# >> palette
# Colores de la página web: A (por omisión) o B, o sus versiones oscuras A-dark y
# B-dark. Cada paleta tiene cinco colores.
# >> mol_rotation
# La vista 3D de una molécula da una vuelta completa en este tiempo (por omisión
# 20s); 0 = se queda quieta (siempre se puede girar con el mouse).
# >> mol_background
# Fondo de la vista 3D: auto (negro con una paleta oscura, blanco con una clara) o un
# color (seis dígitos hexadecimales).
# >> white, black, gray, dark, light
# Reemplaza un color de la paleta (seis dígitos hexadecimales, p. ej. f0f7f4; vacío = el
# de la paleta): white es el fondo, black el texto sobre él (el texto sobre negro es
# blanco), gray las líneas y el texto secundario, dark la pestaña seleccionada, la
# opción elegida y los botones que se pueden usar, light las opciones no elegidas y lo
# que no se puede usar. Se aplica cuando arranca 'matriline-server web'.
#
# ---------------- [update] ----------------
# >> mode
# EXPERIMENTAL, no recomendado para un proyecto en curso: todavía no hay migración de la
# configuración ni de las carpetas, así que una versión que cambiara su formato podría
# desordenar el proyecto. Más seguro: actualizar a mano entre proyectos (INSTALL.md).
# off: nunca busca. alert: una vez por check_interval el servidor busca una versión
# nueva y te avisa (la alerta 'update' de la sección de alertas);
# 'matriline-server update apply' la instala. auto: además la instala por sí solo. La
# instalación solo ocurre si todos los clientes pueden actualizarse (cada uno lo dice al
# conectarse; 'update status' lista los que no pueden), primero los clientes (cada uno
# entre trabajos), luego este servidor, que se reinicia. Solo se instalan versiones
# firmadas con la llave del mantenedor de Matriline, incluida en los programas: ni
# siquiera quien controle la página de versiones o este servidor puede hacer que un
# cliente instale otra cosa. El programa anterior queda como <program>.previous.
# >> check_interval
# Cada cuánto buscar (por omisión 720h, 30 días).
# >> url
# Dónde están las versiones firmadas: la página de versiones de GitHub (o un espejo con
# su estructura: latest/download/release.json, download/v<version>/<program>). Los
# clientes descargan de su propio update_url, no de aquí.
# >> client_wait
# Un cliente que sigue en la versión anterior después de este tiempo (espera a que
# terminen sus trabajos) se notifica; el servidor sigue esperándolo antes de instalar la
# suya.
#
# ---------------- [log] ----------------
# >> level
# debug, info, warn, error
