package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

import androidx.compose.material.icons.automirrored.filled.CallReceived
import androidx.compose.material.icons.automirrored.filled.PhoneCallback

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun CallRoutingRulesScreen(
    onBackClick: () -> Unit = {},
    onNewRuleClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val navyBlue = Color(0xFF1E3A8A)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Call Routing Rules", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    Button(onClick = onNewRuleClick, shape = RoundedCornerShape(8.dp), colors = ButtonDefaults.buttonColors(containerColor = navyBlue)) {
                        Icon(Icons.Default.Add, contentDescription = null, modifier = Modifier.size(16.dp))
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("New Rule", fontSize = 12.sp)
                    }
                }
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(MaterialTheme.colorScheme.background)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            // Header Description
            item {
                Text(
                    "Configure how incoming calls are processed by Aegis AI.",
                    fontSize = 13.sp,
                    color = Color.Gray,
                    modifier = Modifier.fillMaxWidth()
                )
            }

            // Node 1: Incoming Call
            item {
                FlowNodeCard("Incoming Call", "All numbers", Icons.AutoMirrored.Filled.CallReceived, Color(0xFFEEF2FF), Color(0xFF4F46E5))
            }

            item { Icon(Icons.Default.ArrowDownward, contentDescription = null, tint = Color.Gray) }

            // Node 2: Check Caller ID
            item {
                FlowNodeCard("Check Caller ID", "VIP bypass rule", Icons.Default.FilterList, Color(0xFFF3E8FF), Color(0xFF9333EA))
            }

            item { Icon(Icons.Default.ArrowDownward, contentDescription = null, tint = Color.Gray) }

            // Split Decision Nodes
            item {
                Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    // Branch 1: VIP Match Direct
                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = Color(0xFFF0FDF4)),
                        modifier = Modifier.weight(1f).border(1.dp, Color(0xFF86EFAC), RoundedCornerShape(16.dp))
                    ) {
                        Column(modifier = Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Surface(shape = RoundedCornerShape(6.dp), color = Color(0xFFDCFCE7)) {
                                Text("VIP MATCH", modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold, color = Color(0xFF166534))
                            }
                            Icon(Icons.AutoMirrored.Filled.PhoneCallback, contentDescription = null, tint = Color(0xFF16A34A), modifier = Modifier.size(28.dp))
                            Text("Direct Routing", fontWeight = FontWeight.Bold, fontSize = 13.sp)
                            Text("Bypass AI, ring phone", fontSize = 11.sp, color = Color.Gray)
                        }
                    }


                    // Branch 2: Business Hours Check
                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
                        modifier = Modifier.weight(1f).border(1.dp, Color.LightGray.copy(alpha = 0.5f), RoundedCornerShape(16.dp))
                    ) {
                        Column(modifier = Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Icon(Icons.Default.Schedule, contentDescription = null, tint = navyBlue, modifier = Modifier.size(28.dp))
                            Text("Business Hours?", fontWeight = FontWeight.Bold, fontSize = 13.sp)
                            Text("Mon-Fri, 9am - 5pm", fontSize = 11.sp, color = Color.Gray)
                        }
                    }
                }
            }

            item { Icon(Icons.Default.ArrowDownward, contentDescription = null, tint = Color.Gray) }

            // Final Action Nodes (AI Receptionist vs. Voicemail)
            item {
                Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    // YES -> AI Receptionist
                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = Color(0xFFDBEAFE)),
                        modifier = Modifier.weight(1f).border(1.dp, Color(0xFF93C5FD), RoundedCornerShape(16.dp))
                    ) {
                        Column(modifier = Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Surface(shape = RoundedCornerShape(6.dp), color = Color(0xFFBFDBFE)) {
                                Text("YES", modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold, color = Color(0xFF1E40AF))
                            }
                            Icon(Icons.Default.SmartToy, contentDescription = null, tint = Color(0xFF2563EB), modifier = Modifier.size(28.dp))
                            Text("AI Receptionist", fontWeight = FontWeight.Bold, fontSize = 13.sp)
                            Text("Screen & transcribe", fontSize = 11.sp, color = Color.Gray)
                        }
                    }

                    // NO -> Voicemail
                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = Color(0xFFF1F5F9)),
                        modifier = Modifier.weight(1f)
                    ) {
                        Column(modifier = Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Surface(shape = RoundedCornerShape(6.dp), color = Color(0xFFE2E8F0)) {
                                Text("NO", modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold, color = Color(0xFF475569))
                            }
                            Icon(Icons.Default.Voicemail, contentDescription = null, tint = Color(0xFF64748B), modifier = Modifier.size(28.dp))
                            Text("Voicemail", fontWeight = FontWeight.Bold, fontSize = 13.sp)
                            Text("Play after-hours greeting", fontSize = 11.sp, color = Color.Gray)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun FlowNodeCard(title: String, subtitle: String, icon: androidx.compose.ui.graphics.vector.ImageVector, bg: Color, iconColor: Color) {
    Surface(
        shape = RoundedCornerShape(16.dp),
        color = bg,
        modifier = Modifier.width(240.dp)
    ) {
        Row(
            modifier = Modifier.padding(14.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Surface(shape = CircleShape, color = Color.White) {
                Icon(icon, contentDescription = null, tint = iconColor, modifier = Modifier.padding(8.dp).size(20.dp))
            }
            Column {
                Text(title, fontWeight = FontWeight.Bold, fontSize = 13.sp)
                Text(subtitle, fontSize = 11.sp, color = Color.Gray)
            }
        }
    }
}
