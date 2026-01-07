

let i18nCache = {};

// Default Language logic
let storedLang = localStorage.getItem('sms_lang');
let currentLang = storedLang ? storedLang : 'zh-tw'; // Default to zh-tw per user request

// If stored was 'zh', map to 'zh-tw' for compatibility if needed, or just support whatever
if (currentLang === 'zh') currentLang = 'zh-tw';

let auth = {
    username: localStorage.getItem('sms_username'),
    token: localStorage.getItem('sms_token'),
    role: localStorage.getItem('sms_role'), // 'admin' or 'user'
};

$.ajaxSetup({
    beforeSend: function (xhr) {
        if (auth.token) {
            xhr.setRequestHeader('Authorization', 'Bearer ' + auth.token);
        }
    },
    error: function (xhr) {
        if (xhr.status === 401) {
            // Token expired or invalid
            localStorage.removeItem('sms_username');
            localStorage.removeItem('sms_token');
            localStorage.removeItem('sms_role');
            auth = {};
            checkAuth();
        }
    }
});

$(document).ready(function () {
    // Set initial select value
    $('#lang-select').val(currentLang);

    // Init Logic with async I18n load
    loadI18n(currentLang).then(() => {
        checkAuth();
    });

    // Event Listeners
    $('#btn-login').click(doLogin);
    $('#lang-select').change(function () {
        currentLang = $(this).val();
        localStorage.setItem('sms_lang', currentLang);
        loadI18n(currentLang);
    });

    // Nav
    $('.nav-link').click(function (e) {
        e.preventDefault();
        $('.nav-link').removeClass('active');
        $(this).addClass('active');
        $('.view-section').addClass('d-none');

        const id = $(this).attr('id').replace('nav-', 'view-');
        $('#' + id).removeClass('d-none');

        if (id === 'view-sms') loadSMS();
        if (id === 'view-modems') loadModems();
        if (id === 'view-users') loadUsers();
    });

    $('#btn-refresh-sms').click(() => loadSMS(1));
    $('#sms-filter-modem').change(() => loadSMS(1));

    // User Mgmt
    $('#btn-save-user').click(saveUser);

    // Auto Refresh SMS
    setInterval(() => {
        if (!$('#view-sms').hasClass('d-none') && auth.username) {
            // Only refresh if on first page to allow reading logs without jumps?
            // User requested pagination. Usually auto-refresh interrupts pagination.
            // Let's only auto-refresh if on page 1.
            if (currentSMSPage === 1) {
                loadSMS(1);
            }
        }
    }, 10000);

    // AT Terminal Logic
    $('#btn-send-at').click(function () {
        sendATCommand(false);
    });

    $('#btn-send-raw').click(function () {
        sendATCommand(true);
    });

    $('#at-input').keypress(function (e) {
        if (e.which == 13) {
            sendATCommand(false); // Default to AT on Enter
        }
    });
});

function loadI18n(lang) {
    return new Promise((resolve, reject) => {
        if (i18nCache[lang]) {
            applyI18n(i18nCache[lang]);
            resolve();
            return;
        }

        $.getJSON(`/static/i18n/${lang}.json`, function (data) {
            i18nCache[lang] = data;
            applyI18n(data);
            resolve();
        }).fail(function () {
            console.error("Failed to load language: " + lang);
            // Fallback to en if fail?
            if (lang !== 'en') {
                loadI18n('en').then(resolve);
            } else {
                resolve();
            }
        });
    });
}

function applyI18n(data) {
    $('[data-i18n]').each(function () {
        const key = $(this).data('i18n');
        if (data[key]) {
            $(this).text(data[key]);
        }
    });

    // Dynamic strings handling (helper for JS usage)
    window.t = function (key) {
        return data[key] || key;
    };
}

function checkAuth() {
    if (!auth.username) {
        $('#login-app').removeClass('d-none');
        $('#dashboard-app').addClass('d-none');
        return;
    }

    // Show Dashboard
    $('#login-app').addClass('d-none');
    $('#dashboard-app').removeClass('d-none');

    $('#current-user').text(auth.username + " (" + auth.role + ")");

    // Hide Admin Menus if User
    if (auth.role !== 'admin') {
        $('#nav-users').addClass('d-none');
        $('#nav-settings').addClass('d-none');
    } else {
        $('#nav-users').removeClass('d-none');
        $('#nav-settings').removeClass('d-none');
    }

    loadModems(); // Preload for filter
    loadSMS();
}

function doLogin() {
    const u = $('#username').val();
    const p = $('#password').val();
    if (!u || !p) return;

    $('#btn-login').prop('disabled', true).text(window.t('validating') || 'Validating...');

    $.ajax({
        url: '/api/v1/login',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ username: u, password: p }),
        success: function (resp) {
            auth.username = resp.user.username;
            auth.role = resp.user.role;
            auth.token = resp.token;

            localStorage.setItem('sms_username', auth.username);
            localStorage.setItem('sms_role', auth.role);
            localStorage.setItem('sms_token', auth.token); // Not used by backend yet but good practice

            checkAuth();
        },
        error: function () {
            alert("Login Failed");
            $('#btn-login').prop('disabled', false).text("Login");
        }
    });
}

const SMS_LIMIT = 20;

function loadSMS(page = 1) {
    currentSMSPage = page;
    const iccid = $('#sms-filter-modem').val();

    $.get('/api/v1/sms', { iccid: iccid, page: page, limit: SMS_LIMIT }, function (resp) {
        const list = $('#sms-list');
        list.empty();

        const data = resp.data || [];
        const total = resp.total || 0;

        if (data.length === 0) {
            list.append('<div class="text-center text-muted p-3">No messages</div>');
        } else {
            data.forEach(sms => {
                const time = new Date(sms.timestamp).toLocaleString();
                // XSS Protection: Create text node or use .text() 
                // constructing purely via string is risky if content is user input.
                // We will use a safe builder approach.

                const div = $('<div>').addClass('sms-item p-2');
                const header = $('<div>').addClass('d-flex justify-content-between');
                header.append($('<strong>').text(sms.phone));
                header.append($('<small>').addClass('text-muted').text(time));

                const contentDiv = $('<div>').addClass('mb-1').text(sms.content); // Safer .text()

                const footer = $('<small>').addClass('text-secondary').html(`<i class="bi bi-sim"></i> ${sms.iccid}`);

                div.append(header).append(contentDiv).append(footer);
                list.append(div);
            });
        }

        renderPagination(total, page);
    });
}

function renderPagination(total, page) {
    const totalPages = Math.ceil(total / SMS_LIMIT);
    const container = $('#sms-pagination');
    if (!container.length) {
        $('#sms-list').after('<div id="sms-pagination" class="d-flex justify-content-center mt-3 gap-2"></div>');
    }

    const pag = $('#sms-pagination');
    pag.empty();

    if (totalPages <= 1) return;

    // Prev
    const btnPrev = $('<button class="btn btn-sm btn-outline-secondary">Prev</button>');
    if (page <= 1) btnPrev.prop('disabled', true);
    else btnPrev.click(() => loadSMS(page - 1));
    pag.append(btnPrev);

    // Info
    pag.append(`<span class="align-self-center">Page ${page} of ${totalPages}</span>`);

    // Next
    const btnNext = $('<button class="btn btn-sm btn-outline-secondary">Next</button>');
    if (page >= totalPages) btnNext.prop('disabled', true);
    else btnNext.click(() => loadSMS(page + 1));
    pag.append(btnNext);
}

function loadModems() {
    $.get('/api/v1/modems', function (data) {
        const select = $('#sms-filter-modem');
        const currentVal = select.val();
        // Keep "All"
        select.find('option:not(:first)').remove();

        const list = $('#modem-list');
        if (!$('#view-modems').hasClass('d-none')) {
            list.empty();
        }

        // Reset map
        modemMap = {};

        data.forEach(m => {
            modemMap[m.iccid] = m.name || "";

            // Update Filter
            let label = m.iccid;
            if (m.name) label = `${m.name} (${m.iccid})`;
            select.append(`<option value="${m.iccid}">${label}</option>`);

            // Update List View
            if (!$('#view-modems').hasClass('d-none')) {
                const statusClass = m.status === 'online' ? 'online' : 'offline';
                list.append(`
                    <div class="col-md-4 mb-3">
                        <div class="card p-3">
                            <h5><span class="connection-status-dot ${statusClass}"></span> ${m.name ? m.name : m.iccid}</h5>
                            ${m.name ? `<p class="mb-1 text-muted small">${m.iccid}</p>` : ''}
                            <p class="mb-1"><strong>IMEI:</strong> ${m.imei}</p>
                            <p class="mb-1"><strong>${window.t('operator')}:</strong> ${m.operator || 'Not Registered'}</p>
                            <p class="mb-1"><strong>${window.t('registration')}:</strong> ${m.registration || 'Unknown'}</p>
                            <p class="mb-2"><strong>${window.t('signal')}:</strong> ${m.signal_strength > 0 ? m.signal_strength : 'Unknown'}</p>
                            <p class="text-muted small">Port: ${m.port_name}</p>
                            ${auth.role === 'admin' ?
                        `<button class="btn btn-sm btn-outline-secondary w-100 mt-2" onclick="manageWebhooks('${m.iccid}')">${window.t('webhooks') || 'Webhooks'}</button>
                                 <button class="btn btn-sm btn-outline-primary w-100 mt-1" onclick="showModemSettings('${m.iccid}')">${window.t('settings') || 'Settings'}</button>`
                        : ''}
                        </div>
                    </div>
                `);
            }
        });
        select.val(currentVal);
    });
}

function loadUsers() {
    $.get('/api/v1/users', function (data) {
        const body = $('#user-list-body');
        body.empty();
        data.forEach(u => {
            body.append(`
                <tr>
                    <td>${u.username}</td>
                    <td>${u.role}</td>
                    <td>${u.allowed_modems || '*'}</td>
                    <td>
                        <button class="btn btn-sm btn-danger" onclick="deleteUser(${u.id})">Del</button>
                    </td>
                </tr>
            `);
        });
    });
}

window.showAddUser = function () {
    $('#userModal').modal('show');
}

function saveUser() {
    const data = {
        username: $('#u-username').val(),
        password: $('#u-password').val(),
        role: $('#u-role').val(),
        allowed_modems: $('#u-allowed').val()
    };

    $.ajax({
        url: '/api/v1/users',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify(data),
        success: function () {
            $('#userModal').modal('hide');
            loadUsers();
        },
        error: function (err) {
            alert("Error: " + err.responseText);
        }
    });
}

window.deleteUser = function (id) {
    if (confirm("Delete user?")) {
        $.ajax({
            url: '/api/v1/users/' + id,
            method: 'DELETE',
            success: loadUsers
        });
    }
}
// Webhook Modal
let currentICCIDForWebhook = "";

window.manageWebhooks = function (iccid) {
    currentICCIDForWebhook = iccid;
    $('#wh-list-iccid').text(iccid);
    loadWebhooks(iccid);
    $('#webhookListModal').modal('show');
}

function loadWebhooks(iccid) {
    $.get('/api/v1/webhooks?iccid=' + iccid, function (data) { // Ensure using admin route
        const body = $('#wh-list-body');
        body.empty();
        data.forEach(w => {
            body.append(`
                <tr>
                    <td>${w.platform}</td>
                    <td><div class="text-truncate" style="max-width: 150px;" title="${w.url}">${w.url}</div></td>
                    <td>${w.channel_id ? w.channel_id : '-'}</td>
                    <td>${w.template || 'Default'}</td>
                    <td>
                        <button class="btn btn-sm btn-danger" onclick="deleteWebhook(${w.id})"><i class="bi bi-trash"></i></button>
                    </td>
                </tr>
            `);
        });
    });
}

window.showAddWebhook = function () {
    $('#webhookModal').modal('show');
    $('#wh-iccid').val(currentICCIDForWebhook);
    $('#wh-platform').val("generic");
    $('#wh-url').val("");
    $('#wh-channel-id').val("");
    $('#wh-template').val("");
    $('#wh-channel-group').addClass('d-none');
}

$('#wh-platform').change(function () {
    if ($(this).val() === 'telegram') {
        $('#wh-channel-group').removeClass('d-none');
    } else {
        $('#wh-channel-group').addClass('d-none');
    }
});

$('#btn-save-webhook').click(function () {
    const iccid = $('#wh-iccid').val();
    const platform = $('#wh-platform').val();
    const url = $('#wh-url').val();
    const channelId = $('#wh-channel-id').val();
    const template = $('#wh-template').val();

    if (!url) {
        alert("URL is required");
        return;
    }

    const data = {
        iccid: iccid,
        platform: platform,
        url: url,
        channel_id: channelId,
        template: template
    };

    $.ajax({
        url: '/api/v1/webhooks',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify(data),
        success: function () {
            $('#webhookModal').modal('hide');
            loadWebhooks(currentICCIDForWebhook);
        },
        error: function (err) {
            alert("Error: " + err.responseText);
        }
    });
});

window.deleteWebhook = function (id) {
    if (confirm("Delete Webhook?")) {
        $.ajax({
            url: '/api/v1/webhooks/' + id,
            method: 'DELETE',
            success: function () {
                loadWebhooks(currentICCIDForWebhook);
            }
        });
    }
}

// Modem Settings
// Modem Settings
window.showModemSettings = function (iccid) {
    $('#m-iccid-title').text(iccid);
    $('#m-iccid').val(iccid);
    $('#m-name').val("");
    $('#m-operator').val("");
    $('#scan-results').empty();
    $('#at-log').val("");
    $('#at-input').val("");

    // Fetch current details
    $.get('/api/v1/modems/' + iccid, function (m) {
        if (m) {
            $('#m-name').val(m.name || "");
            $('#m-operator').val(m.operator || "");
        }
    });

    $('#modemModal').modal('show');
}

$('#btn-save-modem').click(function () {
    const iccid = $('#m-iccid').val();
    const name = $('#m-name').val();
    // Only saving Name here. Operator is separate buttons.

    $.ajax({
        url: '/api/v1/modems/' + iccid,
        method: 'PUT',
        contentType: 'application/json',
        data: JSON.stringify({ name: name }),
        success: function () {
            $('#modemModal').modal('hide');
            loadModems();
        },
        error: function (err) {
            alert("Error: " + err.responseText);
        }
    });
});

$('#btn-set-operator').click(function () {
    callSetOperator($('#m-operator').val());
});

$('#btn-auto-operator').click(function () {
    callSetOperator("AUTO");
});

function callSetOperator(oper) {
    const iccid = $('#m-iccid').val();
    $.ajax({
        url: '/api/v1/modems/' + iccid + '/operator',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ operator: oper }),
        success: function () {
            alert("Operator update initiated. It may take some time to register.");
            $('#modemModal').modal('hide');
        },
        error: function (err) {
            alert("Failed: " + err.responseText);
        }
    });
}

$('#btn-scan-networks').click(function () {
    const iccid = $('#m-iccid').val();
    const btn = $(this);
    const spinner = $('#scan-spinner');
    const resDiv = $('#scan-results');

    btn.prop('disabled', true);
    spinner.removeClass('d-none');
    resDiv.text("Scanning... this may take up to 2 minutes...");

    $.ajax({
        url: '/api/v1/modems/' + iccid + '/scan',
        method: 'POST',
        success: function (resp) {
            let html = "<ul>";
            if (resp.networks && resp.networks.length > 0) {
                // Expected format: "Name (MCCMNC) [Status]" or raw string
                resp.networks.forEach(n => {
                    // Extract MCCMNC for value if possible
                    // Regex to find (12345)
                    const match = n.match(/\((\d{5,})\)/);
                    let val = "";
                    if (match && match[1]) {
                        val = result = match[1];
                    }

                    if (val) {
                        html += `<li><a href="#" onclick="$('#m-operator').val('${val}'); return false;">${n}</a></li>`;
                    } else {
                        html += `<li>${n}</li>`;
                    }
                });
                html += "</ul><small class='text-muted'>Click network to select</small>";
            } else {
                html += "<li>No networks found</li></ul>";
            }
            resDiv.html(html);
        },
        error: function (err) {
            resDiv.text("Error: " + err.responseText);
        },
        complete: function () {
            btn.prop('disabled', false);
            spinner.addClass('d-none');
        }
    });
});

// Password Change
$('#btn-save-password').click(function () {
    const oldPw = $('#pw-old').val();
    const newPw = $('#pw-new').val();

    $.ajax({
        url: '/api/v1/change_password',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ old_password: oldPw, new_password: newPw }),
        success: function () {
            alert("Password updated");
            $('#passwordModal').modal('hide');
            $('#pw-old').val('');
            $('#pw-new').val('');
        },
        error: function (err) {
            alert("Error: " + err.responseText);
        }
    });
});
// AT Terminal Logic

function sendATCommand(isRaw) {
    const iccid = $('#m-iccid').val();
    const cmd = $('#at-input').val();
    const log = $('#at-log');

    if (!cmd) return;

    log.val(log.val() + `> ${cmd}\n`);
    $('#at-input').val('');

    // Auto-scroll
    log.scrollTop(log[0].scrollHeight);

    // Substitute ^Z to \x1A if raw
    let sentCmd = cmd;
    if (isRaw && cmd.includes('^Z')) {
        sentCmd = cmd.replace('^Z', '\x1A');
    }

    const endpoint = isRaw ? 'input' : 'at';
    const timeout = isRaw ? 5000 : 10000;

    $.ajax({
        url: `/api/v1/modems/${iccid}/${endpoint}`,
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ cmd: sentCmd, timeout: timeout }),
        success: function (resp) {
            log.val(log.val() + `${resp.response}\n`);
            log.scrollTop(log[0].scrollHeight);
        },
        error: function (xhr) {
            let msg = "Error";
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else {
                msg = xhr.responseText;
            }
            log.val(log.val() + `[ERROR] ${msg}\n`);
            log.scrollTop(log[0].scrollHeight);
        }
    });
}
