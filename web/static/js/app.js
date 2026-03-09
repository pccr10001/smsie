

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

let currentSMSPage = 1;
let modemMap = {};
let lastAPIKeySecret = '';

let callWS = null;
let callPC = null;
let callLocalStream = null;
let callWSState = 'idle';
let callStatePollTimer = null;
let modemSettingsRefreshTimer = null;

function closeCallSignaling() {
    if (callLocalStream) {
        callLocalStream.getTracks().forEach(t => t.stop());
        callLocalStream = null;
    }
    if (callPC) {
        callPC.close();
        callPC = null;
    }
    if (callWS) {
        callWS.close();
        callWS = null;
    }
    callWSState = 'idle';
}

function stopCallStatePolling() {
    if (callStatePollTimer) {
        clearInterval(callStatePollTimer);
        callStatePollTimer = null;
    }
}

function normalizeFlag(value) {
    return value === true || value === 'true' || value === 1;
}

function stopModemSettingsRefresh() {
    if (modemSettingsRefreshTimer) {
        clearTimeout(modemSettingsRefreshTimer);
        modemSettingsRefreshTimer = null;
    }
}

function refreshCallStateUI(iccid) {
    $.get('/api/v1/modems/' + iccid + '/call/state', function (state) {
        const hasUACFlag = !!(state && Object.prototype.hasOwnProperty.call(state, 'uac_ready'));
        const uacReady = normalizeFlag(state && state.uac_ready);
        const callState = state && state.state ? String(state.state) : 'idle';
        const callActive = callState === 'dialing' || callState === 'in_call';

        $('#call-status').text(`Call state: ${callState}${state && state.reason ? ` (${state.reason})` : ''}`);

        if (hasUACFlag && !uacReady) {
            $('#call-panel').addClass('d-none');
            $('#call-not-ready').removeClass('d-none').text('UAC is not enabled on this modem. WebRTC calling is unavailable.');
            $('#btn-call-dial').prop('disabled', true);
            $('#btn-call-hangup').prop('disabled', true);
            $('.btn-dtmf').prop('disabled', true);
            return;
        }

        $('#call-not-ready').addClass('d-none').text('');
        $('#call-panel').removeClass('d-none');
        $('#btn-call-dial').prop('disabled', callActive);
        $('#btn-call-hangup').prop('disabled', !callActive);
        $('.btn-dtmf').prop('disabled', !callActive);
    }).fail(function (xhr) {
        const msg = xhr && xhr.responseJSON && xhr.responseJSON.error ? xhr.responseJSON.error : 'call state unavailable';
        $('#call-panel').addClass('d-none');
        $('#call-not-ready').removeClass('d-none').text(msg);
        $('#btn-call-dial').prop('disabled', true);
        $('#btn-call-hangup').prop('disabled', true);
        $('.btn-dtmf').prop('disabled', true);
        $('#call-status').text(`Call state error: ${msg}`);
    });
}

async function ensureCallSignaling(iccid) {
    if (callPC && callWS && callWS.readyState === WebSocket.OPEN && callPC.connectionState === 'connected') {
        return;
    }

    closeCallSignaling();

    const protocol = location.protocol === 'https:' ? 'wss' : 'ws';
    const token = encodeURIComponent(auth.token || '');
    const wsURL = `${protocol}://${location.host}/api/v1/modems/${encodeURIComponent(iccid)}/ws?token=${token}`;

    const stream = await navigator.mediaDevices.getUserMedia({
        audio: {
            channelCount: 1,
            sampleRate: 8000,
            echoCancellation: false,
            noiseSuppression: false,
            autoGainControl: false,
        },
        video: false,
    });
    callLocalStream = stream;

    callWS = new WebSocket(wsURL);
    callWSState = 'connecting';

    callPC = new RTCPeerConnection({ iceServers: [{ urls: 'stun:stun.l.google.com:19302' }] });

    stream.getAudioTracks().forEach(track => callPC.addTrack(track, stream));

    const recvTransceiver = callPC.addTransceiver('audio', { direction: 'recvonly' });
    if (recvTransceiver && recvTransceiver.setCodecPreferences && RTCRtpReceiver.getCapabilities) {
        const caps = RTCRtpReceiver.getCapabilities('audio');
        if (caps && caps.codecs) {
            const selected = caps.codecs.filter((codec) => {
                const mime = (codec.mimeType || '').toLowerCase();
                return mime === 'audio/pcmu' || mime === 'audio/pcma';
            });
            if (selected.length > 0) {
                recvTransceiver.setCodecPreferences(selected);
            }
        }
    }

    callPC.ontrack = (event) => {
        const [remoteStream] = event.streams;
        if (!remoteStream) return;
        const audio = new Audio();
        audio.srcObject = remoteStream;
        audio.autoplay = true;
        audio.play().catch(() => { });
    };

    callPC.onicecandidate = (event) => {
        if (!event.candidate || !callWS || callWS.readyState !== WebSocket.OPEN) return;
        callWS.send(JSON.stringify({ type: 'candidate', candidate: event.candidate }));
    };

    const waitConnected = new Promise((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error('WebRTC connection timeout')), 15000);

        callPC.onconnectionstatechange = () => {
            if (callPC.connectionState === 'connected') {
                clearTimeout(timeout);
                callWSState = 'connected';
                resolve();
            } else if (callPC.connectionState === 'failed' || callPC.connectionState === 'closed') {
                clearTimeout(timeout);
                reject(new Error('WebRTC connection failed'));
            }
        };
    });

    await new Promise((resolve, reject) => {
        callWS.onopen = async () => {
            callWSState = 'ws-open';
            try {
                const offer = await callPC.createOffer({ offerToReceiveAudio: true });
                await callPC.setLocalDescription(offer);
                callWS.send(JSON.stringify({ type: 'offer', offer }));
                resolve();
            } catch (error) {
                reject(error);
            }
        };
        callWS.onerror = () => reject(new Error('WebSocket error'));
        callWS.onclose = () => {
            if (callWSState !== 'connected') {
                reject(new Error('WebSocket closed before connected'));
            }
        };
        callWS.onmessage = async (event) => {
            try {
                const msg = JSON.parse(event.data);
                if (msg.type === 'answer' && msg.answer) {
                    await callPC.setRemoteDescription(msg.answer);
                } else if (msg.type === 'candidate' && msg.candidate) {
                    await callPC.addIceCandidate(msg.candidate);
                } else if (msg.type === 'error') {
                    reject(new Error(msg.text || 'Signal error'));
                }
            } catch (error) {
                reject(error);
            }
        };
    });

    await waitConnected;
}

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

// Country Code Mapping
const countryCodeMap = {
    '1': 'US', '7': 'RU', '20': 'EG', '27': 'ZA', '30': 'GR', '31': 'NL',
    '32': 'BE', '33': 'FR', '34': 'ES', '36': 'HU', '39': 'IT', '40': 'RO',
    '41': 'CH', '43': 'AT', '44': 'GB', '45': 'DK', '46': 'SE', '47': 'NO',
    '48': 'PL', '49': 'DE', '51': 'PE', '52': 'MX', '53': 'CU', '54': 'AR',
    '55': 'BR', '56': 'CL', '57': 'CO', '58': 'VE', '60': 'MY', '61': 'AU',
    '62': 'ID', '63': 'PH', '64': 'NZ', '65': 'SG', '66': 'TH', '81': 'JP',
    '82': 'KR', '84': 'VN', '86': 'CN', '90': 'TR', '91': 'IN', '92': 'PK',
    '93': 'AF', '94': 'LK', '95': 'MM', '98': 'IR', '212': 'MA', '213': 'DZ',
    '216': 'TN', '218': 'LY', '220': 'GM', '221': 'SN', '222': 'MR', '223': 'ML',
    '224': 'GN', '225': 'CI', '226': 'BF', '227': 'NE', '228': 'TG', '229': 'BJ',
    '230': 'MU', '231': 'LR', '232': 'SL', '233': 'GH', '234': 'NG', '235': 'TD',
    '236': 'CF', '237': 'CM', '238': 'CV', '239': 'ST', '240': 'GQ', '241': 'GA',
    '242': 'CG', '243': 'CD', '244': 'AO', '245': 'GW', '248': 'SC', '249': 'SD',
    '250': 'RW', '251': 'ET', '252': 'SO', '253': 'DJ', '254': 'KE', '255': 'TZ',
    '256': 'UG', '257': 'BI', '258': 'MZ', '260': 'ZM', '261': 'MG', '263': 'ZW',
    '264': 'NA', '265': 'MW', '266': 'LS', '267': 'BW', '268': 'SZ', '269': 'KM',
    '290': 'SH', '291': 'ER', '297': 'AW', '298': 'FO', '299': 'GL', '350': 'GI',
    '351': 'PT', '352': 'LU', '353': 'IE', '354': 'IS', '355': 'AL', '356': 'MT',
    '357': 'CY', '358': 'FI', '359': 'BG', '370': 'LT', '371': 'LV', '372': 'EE',
    '373': 'MD', '374': 'AM', '375': 'BY', '376': 'AD', '377': 'MC', '378': 'SM',
    '379': 'VA', '380': 'UA', '381': 'RS', '382': 'ME', '383': 'XK', '385': 'HR',
    '386': 'SI', '387': 'BA', '389': 'MK', '420': 'CZ', '421': 'SK', '423': 'LI',
    '500': 'FK', '501': 'BZ', '502': 'GT', '503': 'SV', '504': 'HN', '505': 'NI',
    '506': 'CR', '507': 'PA', '508': 'PM', '509': 'HT', '590': 'GP', '591': 'BO',
    '592': 'GY', '593': 'EC', '594': 'GF', '595': 'PY', '596': 'MQ', '597': 'SR',
    '598': 'UY', '599': 'CW', '670': 'TL', '673': 'BN', '674': 'NR', '675': 'PG',
    '676': 'TO', '677': 'SB', '678': 'VU', '679': 'FJ', '680': 'PW', '681': 'WF',
    '682': 'CK', '683': 'NU', '685': 'WS', '686': 'KI', '687': 'NC', '688': 'TV',
    '689': 'PF', '690': 'TK', '691': 'FM', '692': 'MH', '850': 'KP', '852': 'HK',
    '853': 'MO', '855': 'KH', '856': 'LA', '880': 'BD', '886': 'TW', '960': 'MV',
    '961': 'LB', '962': 'JO', '963': 'SY', '964': 'IQ', '965': 'KW', '966': 'SA',
    '967': 'YE', '968': 'OM', '970': 'PS', '971': 'AE', '972': 'IL', '973': 'BH',
    '974': 'QA', '975': 'BT', '976': 'MN', '977': 'NP', '992': 'TJ', '993': 'TM',
    '994': 'AZ', '995': 'GE', '996': 'KG', '997': 'KZ', '998': 'UZ'
};

function getFlagFromICCID(iccid) {
    if (!iccid || iccid.length < 5) return "";
    // Clean F if present (legacy)
    if (iccid.toUpperCase().endsWith('F')) {
        iccid = iccid.substring(0, iccid.length - 1);
    }

    // Check prefix 89
    if (!iccid.startsWith('89')) return "";

    const rest = iccid.substring(2);
    // CC is 1-3 digits. Try 3, then 2, then 1.
    for (let len of [3, 2, 1]) {
        const cc = rest.substring(0, len);
        if (countryCodeMap[cc]) {
            return getFlagEmoji(countryCodeMap[cc]);
        }
    }
    return "";
}

function getFlagEmoji(countryCode) {
    if (!countryCode) return "";
    const codePoints = countryCode
        .toUpperCase()
        .split('')
        .map(char => 127397 + char.charCodeAt(0));
    return String.fromCodePoint(...codePoints);
}

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
        if (id === 'view-apikeys') loadAPIKeys();
        if (id === 'view-users') loadUsers();
    });

    $('#btn-refresh-sms').click(() => loadSMS(1));
    $('#sms-filter-modem').change(() => loadSMS(1));
    $('#btn-create-apikey').click(createAPIKey);
    $('#btn-refresh-apikeys').click(loadAPIKeys);
    $('#btn-copy-apikey').click(copyLatestAPIKeySecret);

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
    } else {
        $('#nav-users').removeClass('d-none');
    }

    $('#nav-apikeys').removeClass('d-none');
    renderMCPExamples();
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

                const footer = $('<small>').addClass('text-secondary').html(`<i class="bi bi-sim"></i> ${getFlagFromICCID(sms.iccid)} ${sms.iccid}`);

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
                const workerExists = !(Object.prototype.hasOwnProperty.call(m, 'worker_exists')) || !!m.worker_exists;
                const statusText = workerExists ? (m.status || 'offline') : 'offline';
                const callSupported = !!m.call_supported;
                const sipListenerText = m.sip_listen_port
                    ? `${m.sip_listener_transport || 'SIP'} ${m.sip_listen_port}${m.sip_listener_active ? '' : ' (inactive)'}`
                    : '-';
                const sipListenerLine = callSupported
                    ? `<div class="small text-secondary mb-3">SIP Listener: ${sipListenerText}</div>`
                    : '';

                const commonButtons = `
                    <button class="btn btn-sm btn-outline-secondary" onclick="showSMSModal('${m.iccid}')">SMS</button>
                    ${callSupported ? `<button class="btn btn-sm btn-outline-secondary" onclick="showCallModal('${m.iccid}')">Call</button>` : ''}
                    <button class="btn btn-sm btn-outline-secondary" onclick="showModemSettings('${m.iccid}')">${window.t('settings') || 'Settings'}</button>
                `;
                const adminButtons = auth.role === 'admin'
                    ? `
                        <button class="btn btn-sm btn-outline-secondary" onclick="manageWebhooks('${m.iccid}')">${window.t('webhooks') || 'Webhooks'}</button>
                      `
                    : '';

                list.append(`
                    <article class="modem-card">
                        <div class="d-flex align-items-center justify-content-between mb-2">
                            <h5 class="mb-0"><span class="dot ${statusClass}"></span>${getFlagFromICCID(m.iccid)} ${m.name ? m.name : m.iccid}</h5>
                            <span class="badge text-bg-light text-uppercase">${statusText}</span>
                        </div>
                        ${m.name ? `<div class="mono text-secondary mb-2">${m.iccid}</div>` : ''}
                        <div class="small"><strong>IMEI:</strong> ${m.imei || '-'}</div>
                        <div class="small"><strong>${window.t('operator')}:</strong> ${m.operator || 'Unknown'}</div>
                        <div class="small"><strong>${window.t('registration')}:</strong> ${m.registration || 'Unknown'}</div>
                        <div class="small"><strong>${window.t('signal')}:</strong> ${m.signal_strength > 0 ? `${m.signal_strength}%` : '0%'}</div>
                        <div class="small text-secondary mb-3">Port: ${m.port_name || '-'}</div>
                        ${sipListenerLine}
                        <div class="d-flex flex-wrap gap-2">
                            ${commonButtons}
                            ${adminButtons}
                        </div>
                    </article>
                `);
            }
        });
        select.val(currentVal);
    });
}

window.deleteModem = function (iccid) {
    if (!iccid) return;
    if (!confirm(`Delete ICCID ${iccid}?\\nThis will remove modem profile, SMS history, webhooks, and modem permissions.`)) {
        return;
    }

    $.ajax({
        url: `/api/v1/modems/${encodeURIComponent(iccid)}`,
        method: 'DELETE',
        success: function () {
            loadModems();
            loadSMS(1);
        },
        error: function (xhr) {
            let msg = "Delete failed";
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            alert(msg);
        }
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

function escapeHTML(value) {
    return String(value == null ? '' : value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function formatDateTime(value) {
    if (!value) {
        return '-';
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
        return escapeHTML(value);
    }
    return escapeHTML(date.toLocaleString());
}

function renderAPIKeyPermissions(key) {
    const badges = [];
    if (key.can_view_sms) badges.push('<span class="api-perm-badge">View SMS</span>');
    if (key.can_send_sms) badges.push('<span class="api-perm-badge">Send SMS</span>');
    if (key.can_send_at) badges.push('<span class="api-perm-badge">Send AT</span>');
    if (key.can_make_call) badges.push('<span class="api-perm-badge">Make Call</span>');
    if (!badges.length) {
        return '<span class="text-muted">None</span>';
    }
    return `<div class="api-perm-list">${badges.join('')}</div>`;
}

function renderMCPExamples() {
    const endpoint = `${location.origin}/mcp`;
    $('#mcp-base-url').text(endpoint);
    $('#mcp-example-modems').text(JSON.stringify({
        mcpServers: {
            smsie: {
                type: 'streamable-http',
                url: endpoint,
                headers: {
                    Authorization: 'Bearer smsie_xxx'
                }
            }
        }
    }, null, 2));
    $('#mcp-example-list-sms').text([
        'list_modems',
        'list_sms',
        'wait_sms',
        'send_sms'
    ].join(''));
    $('#mcp-example-wait-sms').text(JSON.stringify({
        jsonrpc: '2.0',
        id: 2,
        method: 'tools/call',
        params: {
            name: 'list_sms',
            arguments: {
                iccid: 'YOUR_ICCID',
                page: 1,
                page_size: 20,
                max_records: 100,
                type: 'received'
            }
        }
    }, null, 2));
    $('#mcp-example-send-sms').text('Initialize with POST /mcp, keep the returned Mcp-Session-Id header, then let your MCP client call tools over the same Streamable HTTP session. GET /mcp opens the optional SSE stream, and DELETE /mcp closes the session.');
}

function showAPIKeySecret(secret) {
    lastAPIKeySecret = secret || '';
    if (!lastAPIKeySecret) {
        $('#apikey-secret-panel').addClass('d-none');
        $('#apikey-secret-value').text('');
        return;
    }
    $('#apikey-secret-value').text(lastAPIKeySecret);
    $('#apikey-secret-panel').removeClass('d-none');
}

function copyLatestAPIKeySecret() {
    if (!lastAPIKeySecret) {
        return;
    }
    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(lastAPIKeySecret).then(function () {
            $('#btn-copy-apikey').text('Copied');
            setTimeout(function () {
                $('#btn-copy-apikey').html('<i class="bi bi-clipboard"></i> Copy');
            }, 1500);
        });
        return;
    }
    window.prompt('Copy API key', lastAPIKeySecret);
}

function resetAPIKeyForm() {
    $('#apikey-name').val('');
    $('#apikey-expires-at').val('');
    $('#apikey-can-view-sms').prop('checked', true);
    $('#apikey-can-send-sms').prop('checked', true);
    $('#apikey-can-send-at').prop('checked', false);
    $('#apikey-can-make-call').prop('checked', false);
}

function loadAPIKeys() {
    renderMCPExamples();
    $.get('/api/v1/apikeys', function (data) {
        const body = $('#apikey-list-body');
        body.empty();

        if (!Array.isArray(data) || data.length === 0) {
            body.append('<tr><td colspan="6" class="text-center text-muted py-4">No API keys yet.</td></tr>');
            return;
        }

        data.forEach(key => {
            body.append(`
                <tr>
                    <td>${escapeHTML(key.name || 'default')}</td>
                    <td class="mono">${escapeHTML(key.key_prefix || '-')}</td>
                    <td>${renderAPIKeyPermissions(key)}</td>
                    <td>${formatDateTime(key.expires_at)}</td>
                    <td>${formatDateTime(key.last_used_at)}</td>
                    <td>
                        <div class="d-flex gap-2 flex-wrap">
                            <button class="btn btn-sm btn-outline-secondary" onclick="rotateAPIKey(${key.id})"><i class="bi bi-arrow-repeat"></i> Rotate</button>
                            <button class="btn btn-sm btn-outline-danger" onclick="deleteAPIKey(${key.id})"><i class="bi bi-trash"></i> Delete</button>
                        </div>
                    </td>
                </tr>
            `);
        });
    }).fail(function (xhr) {
        let msg = 'Failed to load API keys';
        if (xhr.responseJSON && xhr.responseJSON.error) {
            msg = xhr.responseJSON.error;
        }
        $('#apikey-list-body').html(`<tr><td colspan="6" class="text-center text-danger py-4">${escapeHTML(msg)}</td></tr>`);
    });
}

function createAPIKey() {
    const btn = $('#btn-create-apikey');
    const status = $('#apikey-create-status');
    const expiresAtRaw = $('#apikey-expires-at').val();
    let expiresAt = '';

    if (expiresAtRaw) {
        const parsed = new Date(expiresAtRaw);
        if (Number.isNaN(parsed.getTime())) {
            status.html('<span class="text-danger">Invalid expires time.</span>');
            return;
        }
        expiresAt = parsed.toISOString();
    }

    const payload = {
        name: $('#apikey-name').val().trim(),
        can_view_sms: $('#apikey-can-view-sms').is(':checked'),
        can_send_sms: $('#apikey-can-send-sms').is(':checked'),
        can_send_at: $('#apikey-can-send-at').is(':checked'),
        can_make_call: $('#apikey-can-make-call').is(':checked')
    };
    if (expiresAt) {
        payload.expires_at = expiresAt;
    }

    btn.prop('disabled', true);
    status.html('<span class="text-muted">Creating API key...</span>');

    $.ajax({
        url: '/api/v1/apikeys',
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify(payload),
        success: function (resp) {
            status.html('<span class="text-success">API key created.</span>');
            resetAPIKeyForm();
            showAPIKeySecret(resp.api_key || '');
            loadAPIKeys();
        },
        error: function (xhr) {
            let msg = 'Failed to create API key';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            status.html(`<span class="text-danger">${escapeHTML(msg)}</span>`);
        },
        complete: function () {
            btn.prop('disabled', false);
        }
    });
}

window.rotateAPIKey = function (id) {
    if (!confirm('Rotate this API key? The old secret will stop working immediately.')) {
        return;
    }
    $.ajax({
        url: `/api/v1/apikeys/${id}/rotate`,
        method: 'POST',
        success: function (resp) {
            showAPIKeySecret(resp.api_key || '');
            loadAPIKeys();
        },
        error: function (xhr) {
            let msg = 'Failed to rotate API key';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            alert(msg);
        }
    });
}

window.deleteAPIKey = function (id) {
    if (!confirm('Delete this API key?')) {
        return;
    }
    $.ajax({
        url: `/api/v1/apikeys/${id}`,
        method: 'DELETE',
        success: function () {
            loadAPIKeys();
        },
        error: function (xhr) {
            let msg = 'Failed to delete API key';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            alert(msg);
        }
    });
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
function updateModemSIPFieldVisibility() {
    const enabled = $('#m-sip-enabled').is(':checked');
    const acceptIncoming = $('#m-sip-accept-incoming').is(':checked');
    $('#m-sip-fields').toggleClass('d-none', !enabled);
    $('#m-sip-incoming-target').prop('disabled', !enabled || !acceptIncoming);
    $('#m-sip-incoming-target-row').toggleClass('opacity-50', !enabled || !acceptIncoming);
}

function formatSIPStatusTime(value) {
    if (!value) {
        return '';
    }
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) {
        return '';
    }
    return parsed.toLocaleString();
}

function describeModemSIPStatus(modem) {
    const enabled = normalizeFlag(modem && modem.sip_enabled);
    const workerExists = normalizeFlag(modem && modem.worker_exists);
    const uacReady = normalizeFlag(modem && modem.call_supported);
    const username = String(modem && modem.sip_username ? modem.sip_username : '').trim();
    const proxy = String(modem && modem.sip_proxy ? modem.sip_proxy : '').trim();
    const state = String(modem && modem.sip_register_state ? modem.sip_register_state : '').trim().toLowerCase();
    const reason = String(modem && modem.sip_register_reason ? modem.sip_register_reason : '').trim();
    let summary = 'Inactive';

    if (!enabled) {
        summary = 'Disabled';
    } else if (!workerExists) {
        summary = 'Modem offline';
    } else if (!uacReady) {
        summary = 'UAC unavailable';
    } else if (!username || !proxy) {
        summary = 'Incomplete settings';
    } else if (state == 'registered') {
        summary = 'Registered';
    } else if (state == 'connecting') {
        summary = 'Connecting';
    } else if (state == 'error') {
        summary = reason ? `Error: ${reason}` : 'Error';
    } else if (state) {
        summary = state.charAt(0).toUpperCase() + state.slice(1);
    } else if (modem && modem.sip_listener_active) {
        summary = 'Listener active';
    } else if (modem && modem.sip_listen_port) {
        summary = 'Assigned / inactive';
    }

    let detail = summary;
    if (reason && !detail.toLowerCase().includes(reason.toLowerCase())) {
        detail += ` (${reason})`;
    }
    const updatedAt = formatSIPStatusTime(modem && modem.sip_register_updated_at ? modem.sip_register_updated_at : '');
    if (updatedAt) {
        detail += ` @ ${updatedAt}`;
    }

    return { summary: summary, detail: detail };
}

function syncModemSIPUI(modem) {
    const current = Object.assign({}, modem || {}, {
        sip_enabled: $('#m-sip-enabled').is(':checked'),
        sip_username: $('#m-sip-username').val(),
        sip_proxy: $('#m-sip-proxy').val(),
        sip_transport: $('#m-sip-transport-select').val(),
        sip_accept_incoming: $('#m-sip-accept-incoming').is(':checked'),
        sip_invite_target: $('#m-sip-incoming-target').val(),
    });
    const status = describeModemSIPStatus(current);
    const displayedTransport = current.sip_listener_transport
        ? String(current.sip_listener_transport).toUpperCase()
        : String(current.sip_transport || 'udp').toUpperCase();

    updateModemSIPFieldVisibility();
    $('#m-sip-status').val(status.summary);
    $('#m-sip-runtime-status').val(status.detail);
    $('#m-sip-port-display').val(current.sip_listen_port ? current.sip_listen_port : '-');
    $('#m-sip-transport').val(displayedTransport);

    let warning = '';
    if (!normalizeFlag(current.worker_exists)) {
        warning = 'Modem is offline. SIP client will remain stopped until this ICCID is detected again.';
    } else if (!normalizeFlag(current.call_supported)) {
        warning = 'This modem does not expose UAC audio right now. SIP and WebRTC calling are disabled until a UAC-capable modem is attached.';
    } else if (normalizeFlag(current.sip_enabled) && (!String(current.sip_username || '').trim() || !String(current.sip_proxy || '').trim())) {
        warning = 'Username and SIP Proxy are required before registration can succeed.';
    } else if (normalizeFlag(current.sip_enabled) && normalizeFlag(current.sip_accept_incoming) && !String(current.sip_invite_target || '').trim()) {
        warning = 'Incoming forwarding is enabled, but Incoming Invite Target is blank. Incoming modem calls will not be forwarded until a target is set.';
    }

    $('#m-sip-warning').toggleClass('d-none', warning === '').text(warning);
}

function populateModemSettingsForm(modem) {
    const current = modem || {};
    $('#modemModal').data('modem', current);
    $('#m-name').val(current.name || '');
    $('#m-operator').val(current.operator || '');
    $('#m-sip-enabled').prop('checked', normalizeFlag(current.sip_enabled));
    $('#m-sip-username').val(current.sip_username || '');
    $('#m-sip-password').val('');
    $('#m-sip-password').attr('placeholder', normalizeFlag(current.sip_has_password) ? 'Leave blank to keep current password' : 'Optional');
    $('#m-sip-proxy').val(current.sip_proxy || '');
    $('#m-sip-server-port').val(current.sip_port || '');
    $('#m-sip-domain').val(current.sip_domain || '');
    $('#m-sip-transport-select').val(String(current.sip_transport || 'udp').toLowerCase());
    $('#m-sip-port').val(current.sip_listen_port || 0);
    $('#m-sip-register').prop('checked', !Object.prototype.hasOwnProperty.call(current, 'sip_register') || normalizeFlag(current.sip_register));
    $('#m-sip-skip-verify').prop('checked', normalizeFlag(current.sip_tls_skip_verify));
    $('#m-sip-accept-incoming').prop('checked', normalizeFlag(current.sip_accept_incoming));
    $('#m-sip-incoming-target').val(current.sip_invite_target || '');
    syncModemSIPUI(current);
}

function loadModemSettingsIntoModal(iccid) {
    $.get('/api/v1/modems/' + iccid, function (modem) {
        populateModemSettingsForm(modem || {});
    }).fail(function (xhr) {
        const msg = xhr && xhr.responseJSON && xhr.responseJSON.error ? xhr.responseJSON.error : 'Failed to load modem settings';
        $('#modem-status').html(`<span class="text-danger">${msg}</span>`);
    });
}

window.showModemSettings = function (iccid) {
    stopModemSettingsRefresh();
    $('#m-iccid-title').text(iccid);
    $('#m-iccid').val(iccid);
    $('#m-name').val('');
    $('#m-operator').val('');
    $('#m-sip-enabled').prop('checked', false);
    $('#m-sip-username').val('');
    $('#m-sip-password').val('');
    $('#m-sip-proxy').val('');
    $('#m-sip-server-port').val('');
    $('#m-sip-domain').val('');
    $('#m-sip-transport-select').val('udp');
    $('#m-sip-port').val(0);
    $('#m-sip-register').prop('checked', true);
    $('#m-sip-skip-verify').prop('checked', false);
    $('#m-sip-accept-incoming').prop('checked', false);
    $('#m-sip-incoming-target').val('');
    $('#m-sip-port-display').val('-');
    $('#m-sip-transport').val('UDP');
    $('#m-sip-status').val('Inactive');
    $('#m-sip-runtime-status').val('Inactive');
    $('#m-sip-warning').addClass('d-none').text('');
    $('#scan-results').empty();
    $('#at-log').val('');
    $('#at-input').val('');
    $('#modem-status').text('');
    $('#modemModal').data('modem', { iccid: iccid, sip_transport: 'udp' });
    updateModemSIPFieldVisibility();

    if (auth.role === 'admin') {
        $('#btn-delete-modem').removeClass('d-none').prop('disabled', false);
    } else {
        $('#btn-delete-modem').addClass('d-none');
    }

    $('#modemModal').modal('show');
    loadModemSettingsIntoModal(iccid);
}

$(document).on('change input', '#m-sip-enabled, #m-sip-username, #m-sip-proxy, #m-sip-transport-select, #m-sip-accept-incoming, #m-sip-incoming-target', function () {
    syncModemSIPUI($('#modemModal').data('modem') || {});
});

window.showSMSModal = function (iccid) {
    $('#sms-iccid-title').text(iccid);
    $('#sms-iccid').val(iccid);
    $('#sms-phone').val('');
    $('#sms-content').val('');
    $('#sms-send-status').empty();
    $('#smsModal').modal('show');
}

$('#modemModal').on('hidden.bs.modal', function () {
    stopModemSettingsRefresh();
    $('#modem-status').text('');
});

$(document).on('click', '#btn-delete-modem', function () {
    const iccid = $('#m-iccid').val();
    if (!iccid) return;
    $('#modemModal').modal('hide');
    deleteModem(iccid);
});

window.showCallModal = function (iccid) {
    $('#call-iccid-title').text(iccid);
    $('#call-iccid').val(iccid);
    $('#call-phone').val('');
    $('#call-status').text('Call state: idle');
    $('#call-panel').addClass('d-none');
    $('#call-not-ready').addClass('d-none').text('');
    stopCallStatePolling();
    closeCallSignaling();

    refreshCallStateUI(iccid);
    callStatePollTimer = setInterval(function () {
        if ($('#callModal').hasClass('show')) {
            refreshCallStateUI(iccid);
        }
    }, 2000);

    $('#callModal').modal('show');
}

$('#callModal').on('hidden.bs.modal', function () {
    stopCallStatePolling();
    closeCallSignaling();
});

$(document).on('click', '#btn-call-dial', function () {
    const iccid = $('#call-iccid').val();
    const number = $('#call-phone').val().trim();
    const statusDiv = $('#call-status');
    const dialBtn = $(this);
    const hangupBtn = $('#btn-call-hangup');

    if (!number) {
        statusDiv.html('<span class="text-danger">Please enter a phone number</span>');
        return;
    }

    dialBtn.prop('disabled', true);
    hangupBtn.prop('disabled', true);
    statusDiv.html('<span class="text-muted">Initializing microphone/WebRTC...</span>');

    (async () => {
        try {
            await ensureCallSignaling(iccid);
        } catch (error) {
            statusDiv.html(`<span class="text-danger">${error.message || 'WebRTC init failed'}</span>`);
            dialBtn.prop('disabled', false);
            hangupBtn.prop('disabled', true);
            return;
        }

        statusDiv.html('<span class="text-muted">Dialing...</span>');

        $.ajax({
            url: `/api/v1/modems/${iccid}/call/dial`,
            method: 'POST',
            contentType: 'application/json',
            data: JSON.stringify({ number: number }),
            success: function (resp) {
                const state = resp && resp.call_state ? resp.call_state.state : 'dialing';
                const reason = resp && resp.call_state ? resp.call_state.reason : '';
                statusDiv.html(`<span class="text-success">Call state: ${state}${reason ? ` (${reason})` : ''}</span>`);
                refreshCallStateUI(iccid);
            },
            error: function (xhr) {
                let msg = 'Dial failed';
                if (xhr.responseJSON && xhr.responseJSON.error) {
                    msg = xhr.responseJSON.error;
                } else if (xhr.responseText) {
                    msg = xhr.responseText;
                }
                closeCallSignaling();
                statusDiv.html(`<span class="text-danger">${msg}</span>`);
                refreshCallStateUI(iccid);
            },
            complete: function () {
                dialBtn.prop('disabled', false);
                hangupBtn.prop('disabled', false);
            }
        });
    })();
});

$(document).on('click', '#btn-call-hangup', function () {
    const iccid = $('#call-iccid').val();
    const statusDiv = $('#call-status');
    const dialBtn = $('#btn-call-dial');
    const hangupBtn = $(this);

    dialBtn.prop('disabled', true);
    hangupBtn.prop('disabled', true);
    statusDiv.html('<span class="text-muted">Hanging up...</span>');

    $.ajax({
        url: `/api/v1/modems/${iccid}/call/hangup`,
        method: 'POST',
        contentType: 'application/json',
        data: '{}',
        success: function (resp) {
            const state = resp && resp.call_state ? resp.call_state.state : 'idle';
            const reason = resp && resp.call_state ? resp.call_state.reason : '';
            statusDiv.html(`<span class="text-success">Call state: ${state}${reason ? ` (${reason})` : ''}</span>`);
            closeCallSignaling();
            refreshCallStateUI(iccid);
        },
        error: function (xhr) {
            let msg = 'Hangup failed';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            statusDiv.html(`<span class="text-danger">${msg}</span>`);
            refreshCallStateUI(iccid);
        },
        complete: function () {
            dialBtn.prop('disabled', false);
            hangupBtn.prop('disabled', false);
        }
    });
});

$(document).on('click', '.btn-dtmf', function () {
    const iccid = $('#call-iccid').val();
    const tone = String($(this).data('tone') || '').trim();
    const statusDiv = $('#call-status');

    if (!/^[0-9*#]$/.test(tone)) {
        return;
    }
    if (!iccid) {
        statusDiv.html('<span class="text-danger">Modem not selected</span>');
        return;
    }

    $.ajax({
        url: `/api/v1/modems/${iccid}/call/dtmf`,
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ tone: tone }),
        success: function () {
            statusDiv.html(`<span class="text-muted">DTMF sent: ${tone}</span>`);
        },
        error: function (xhr) {
            let msg = 'DTMF failed';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            statusDiv.html(`<span class="text-danger">${msg}</span>`);
        }
    });
});

$(document).on('click', '#btn-reboot-modem', function () {
    const iccid = $('#m-iccid').val();
    const btn = $(this);
    const statusDiv = $('#modem-status');

    if (!confirm('Reboot modem now? (AT+CFUN=1,1)')) {
        return;
    }

    btn.prop('disabled', true).text('Rebooting...');
    statusDiv.html('<span class="text-warning">Sending reboot command...</span>');

    $.ajax({
        url: `/api/v1/modems/${iccid}/reboot`,
        method: 'POST',
        success: function () {
            closeCallSignaling();
            statusDiv.html('<span class="text-success">Reboot command sent. Wait for modem to reconnect.</span>');
        },
        error: function (xhr) {
            let msg = 'Reboot failed';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            statusDiv.html(`<span class="text-danger">${msg}</span>`);
        },
        complete: function () {
            btn.prop('disabled', false).text('Reboot Modem (AT+CFUN=1,1)');
        }
    });
});

// Send SMS Handler
$('#btn-send-sms').click(function () {
    const iccid = $('#sms-iccid').val();
    const phone = $('#sms-phone').val().trim();
    const message = $('#sms-content').val().trim();
    const statusDiv = $('#sms-send-status');
    const btn = $(this);

    if (!phone) {
        statusDiv.html('<span class="text-danger">Please enter a phone number</span>');
        return;
    }
    if (!message) {
        statusDiv.html('<span class="text-danger">Please enter a message</span>');
        return;
    }

    btn.prop('disabled', true).html('<span class="spinner-border spinner-border-sm"></span> Sending...');
    statusDiv.html('<span class="text-muted">Sending SMS...</span>');

    $.ajax({
        url: `/api/v1/modems/${iccid}/send`,
        method: 'POST',
        contentType: 'application/json',
        data: JSON.stringify({ phone: phone, message: message }),
        success: function (resp) {
            statusDiv.html('<span class="text-success"><i class="bi bi-check-circle"></i> SMS sent successfully!</span>');
            // Clear form on success
            $('#sms-phone').val("");
            $('#sms-content').val("");
        },
        error: function (xhr) {
            let msg = "Failed to send SMS";
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            statusDiv.html(`<span class="text-danger"><i class="bi bi-x-circle"></i> ${msg}</span>`);
        },
        complete: function () {
            btn.prop('disabled', false).html('<i class="bi bi-send"></i> Send SMS');
        }
    });
});

$('#btn-save-modem').click(function () {
    const iccid = $('#m-iccid').val();
    const statusDiv = $('#modem-status');
    const sipPort = parseInt($('#m-sip-server-port').val(), 10) || 0;
    const sipListenPort = parseInt($('#m-sip-port').val(), 10) || 0;
    const payload = {
        name: $('#m-name').val(),
        sip_enabled: $('#m-sip-enabled').is(':checked'),
        sip_username: $('#m-sip-username').val().trim(),
        sip_password: $('#m-sip-password').val(),
        sip_proxy: $('#m-sip-proxy').val().trim(),
        sip_port: sipPort,
        sip_domain: $('#m-sip-domain').val().trim(),
        sip_transport: String($('#m-sip-transport-select').val() || 'udp').trim().toLowerCase(),
        sip_register: $('#m-sip-register').is(':checked'),
        sip_tls_skip_verify: $('#m-sip-skip-verify').is(':checked'),
        sip_accept_incoming: $('#m-sip-accept-incoming').is(':checked'),
        sip_invite_target: $('#m-sip-incoming-target').val().trim(),
        sip_listen_port: sipListenPort,
    };

    if (payload.sip_port < 0 || payload.sip_port > 65535 || payload.sip_listen_port < 0 || payload.sip_listen_port > 65535) {
        statusDiv.html('<span class="text-danger">SIP ports must be between 0 and 65535.</span>');
        return;
    }

    statusDiv.html('<span class="text-muted">Saving modem settings...</span>');
    stopModemSettingsRefresh();

    $.ajax({
        url: '/api/v1/modems/' + iccid,
        method: 'PUT',
        contentType: 'application/json',
        data: JSON.stringify(payload),
        success: function (resp) {
            populateModemSettingsForm(resp || {});
            loadModems();
            statusDiv.html('<span class="text-success">Settings saved. Refreshing SIP runtime status...</span>');
            modemSettingsRefreshTimer = setTimeout(function () {
                if ($('#modemModal').hasClass('show') && $('#m-iccid').val() === iccid) {
                    loadModemSettingsIntoModal(iccid);
                    $('#modem-status').html('<span class="text-success">Settings saved.</span>');
                }
            }, 3500);
        },
        error: function (xhr) {
            let msg = 'Failed to save modem settings';
            if (xhr.responseJSON && xhr.responseJSON.error) {
                msg = xhr.responseJSON.error;
            } else if (xhr.responseText) {
                msg = xhr.responseText;
            }
            statusDiv.html(`<span class="text-danger">${msg}</span>`);
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
