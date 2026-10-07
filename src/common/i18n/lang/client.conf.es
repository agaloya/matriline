# source a16b9507457affa7
# =============================================================================
# Configuración del cliente Matriline (opciones LOCALES de esta computadora)
# =============================================================================
# La política de la red (cifrado, cómo inscribirse, qué archivos se devuelven, ...) la
# decide el servidor y se recibe automáticamente: no se configura aquí, y el servidor no
# puede hacer que esta computadora supere los límites que fijes abajo.


[general]
# en: language of the menus, help and messages: "en", "es", "fr", "pt", "ar" (empty: this computer's)
# es: idioma de los menús, la ayuda y los mensajes: "en", "es", "fr", "pt", "ar" (vacío: el de esta computadora)
# fr: langue des menus, de l'aide et des messages : "en", "es", "fr", "pt", "ar" (vide : celle de cet ordinateur)
# pt: idioma dos menus, da ajuda e das mensagens: "en", "es", "fr", "pt", "ar" (vazio: o deste computador)
# ar: لغة القوائم والمساعدة والرسائل: "en", "es", "fr", "pt", "ar" (فارغ: لغة هذا الحاسوب؛ الترجمة العربية آلية ولم تُراجَع بعد)
language =


[client]
# Archivo de credencial que te dio el administrador del servidor (o creado por
# "matriline-client join").
credential = credential.conf

# Dónde guarda el cliente su estado (los trabajos en curso sobreviven aquí a los cortes
# de luz).
state_dir = state

# Solo puede correr un cliente por state_dir (un segundo se niega a arrancar). Para
# prestar esta computadora también a otro proyecto, corre un segundo cliente con su
# propio directorio.
# Dónde corre ORCA. Vacío = <state_dir>/jobs. Usa un disco local rápido.
scratch_dir =


[orca]
# Instalaciones de ORCA disponibles en esta computadora (el servidor dice qué versión
# usar).
paths = /opt/orca-6.1.1

# Instalación de OpenMPI para trabajos de varios núcleos (resources.max_cores_per_job >
# 1): el directorio que contiene bin/mpirun. "auto" busca en PATH y en /opt/openmpi-*.
# ORCA 6.1 para Linux necesita OpenMPI 4.1.x. Se le calcula la huella como a ORCA y
# corre dentro del mismo entorno aislado (sandbox).
mpi_path = auto


[resources]
# Núcleos de CPU prestados a Matriline (un cálculo de un núcleo por cada núcleo).
# 0 = automático: todos los núcleos físicos menos uno (una computadora de un solo núcleo
# usa ese núcleo y avisa).
cores = 0

# smt: contar también el segundo hilo de hardware de cada núcleo (Hyper-Threading/SMT)
# como un núcleo. Desactivado por omisión: medido con ORCA, un segundo trabajo en el
# mismo núcleo solo añade ~11 % de rendimiento y hace cada trabajo ~80 % más lento y la
# CPU más caliente.
smt = false

# Memoria por núcleo. El cliente fija el %maxcore de ORCA en el 75 % de este valor (ORCA
# puede pasarse de %maxcore) y avisa y presta menos núcleos si la computadora no tiene
# suficiente RAM.
memory_per_core = 1GiB

# O un total para todos los trabajos juntos (p. ej. 12GiB; 0 = desactivado): cada
# trabajo recibe una parte igual (memory_total / cores) por omisión, pero un trabajo que
# se sabe que necesita más memoria por núcleo (el servidor lo aprende de los propios
# errores de ORCA) o un trabajo de varios núcleos puede tomar más mientras el resto esté
# libre. Si se fija, memory_per_core no se usa.
memory_total = 0

# Cálculo de varios núcleos más grande que se acepta (una entrada con "%pal nprocs N" o
# "! PALN", cuando el servidor los permite). Un trabajo de N núcleos toma N de los
# núcleos de arriba y N veces la memoria por núcleo. 1 = solo trabajos de un núcleo (por
# omisión: varios trabajos independientes aprovechan mejor los núcleos; medido, 2
# núcleos hacen un trabajo solo ~1.25x más rápido). Necesita OpenMPI, ver orca.mpi_path.
# macOS: siempre 1. Su entorno aislado no puede limitar las conexiones de OpenMPI a esta
# computadora, así que un trabajo de varios núcleos tendría acceso a la red.
max_cores_per_job = 1

# Disco de trabajo para los cálculos en curso, igual que los núcleos y la memoria: un
# total para todos los trabajos juntos (disk_limit; no hay tareas nuevas mientras esté
# agotado), y/o una cantidad fija por trabajo (disk_per_job; un trabajo que necesite más
# se detiene y pasa a otra computadora). Ambos sin límite por omisión: el cliente
# aprende cuánto necesita cada tipo de trabajo y solo toma un trabajo que quepa.
disk_limit = unlimited

disk_per_job = unlimited

# Lo que siempre se deja libre en el disco de trabajo para el sistema y el usuario,
# digan lo que digan los límites de arriba; un trabajo en curso se detiene si el espacio
# libre baja de la mitad de esto.
# "auto" = 10 GiB, pero como mucho el 20 % del disco (p. ej. 2 GiB en un disco de 10 GB);
# o un tamaño.
disk_reserve = auto

# Límite de velocidad de subida de los resultados (10Mbit por omisión; "unlimited" para
# quitarlo).
bandwidth_limit = 10Mbit


[schedule]
# Solo acepta tareas nuevas dentro de estas ventanas (las que corren siempre terminan).
# Vacío = siempre.
# Ejemplos: windows = mon-fri 20:00-07:00, sat-sun 00:00-24:00
windows =

# No acepta tareas nuevas mientras funciona con batería.
pause_on_battery = true

# Mientras corren cálculos con la computadora conectada a la corriente, evita que se
# suspenda sola (la pantalla puede apagarse; cerrar la tapa de una laptop la suspende igual).
# Con batería o sin nada que calcular se suspende como siempre. No cambia su configuración
# de energía.
keep_awake = true

# Con pause_on_battery = false: deja de aceptar tareas nuevas con batería por debajo de
# esta carga (porcentaje, 0 = desactivado); las tareas en curso terminan. Vuelve a tomar
# tareas al conectarse a la corriente o cuando la carga sube battery_resume_margin
# puntos por encima del límite (sin oscilar alrededor de él). Las máquinas sin batería
# del sistema (escritorios, servidores, máquinas virtuales) nunca se pausan por esto.
min_battery_percent = 0

battery_resume_margin = 5

# No acepta tareas nuevas mientras otros programas usen más de este % de la CPU (0 =
# desactivado).
pause_when_busy_percent = 0


[limits]
# Detiene un cálculo que dura más que esto (0 = sin límite). La tarea vuelve al servidor
# para otra computadora.
max_task_time = 0

# Archivo individual más grande que puede escribir un cálculo (p. ej. 50G), sin límite
# por omisión. Los archivos temporales de ORCA pueden ocupar varios GB (DLPNO); la
# protección del disco de trabajo cuida el disco.
max_file_size = unlimited

# Deja de aceptar tareas nuevas mientras la CPU esté más caliente que esto (grados
# Celsius,
# 0 = desactivado, por omisión). Los cálculos en curso continúan; los nuevos se reanudan
# cuando se enfría.
cpu_temperature_limit = 0

# De dónde se lee la temperatura. Vacío = el sensor de CPU de esta computadora. Un
# archivo con un número en grados Celsius sirve en una máquina virtual (que no tiene
# sensor) cuando su anfitrión escribe ahí su propia temperatura de CPU, o para cualquier
# sensor propio.
cpu_temperature_file =

# Después de esta fecha (AAAA-MM-DD) el cliente se detiene y se desactiva. "never" por
# omisión.
end_date = never


[security]
# Corre ORCA aislado: sin acceso a la red, sin acceso a tus archivos fuera del
# directorio del trabajo y sin más programas que la instalación de ORCA. Muy
# recomendable.
sandbox = true

# Cada cuánto se vuelve a calcular el hash de toda la instalación de ORCA, sin usar la
# caché.
orca_deep_check_interval = 24h

# Instala las versiones nuevas de Matriline cuando el servidor las ofrece. Solo se
# instalan versiones firmadas con la llave del mantenedor de Matriline (incluida en este
# programa), las envíe quien las envíe; ocurre entre trabajos, y el programa anterior
# queda como <program>.previous.
updates = true

# De dónde se descargan las versiones nuevas (el servidor solo dice qué versión; nunca
# elige la dirección).
update_url = https://github.com/agaloya/matriline/releases